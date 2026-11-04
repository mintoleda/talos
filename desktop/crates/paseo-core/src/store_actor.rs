//! Bounded bridge from async orchestration to the synchronous SQLite store.

use std::{
    path::PathBuf,
    sync::{Arc, Mutex},
    thread::JoinHandle,
};

use paseo_store::{Result as StoreResult, SqliteStore};
use tokio::sync::{mpsc, oneshot};

const STORE_QUEUE_CAPACITY: usize = 128;

type StoreTask = Box<dyn FnOnce(&mut SqliteStore) + Send + 'static>;

enum Message {
    Run(StoreTask),
    Shutdown,
}

pub(crate) struct StoreActor {
    sender: Arc<Mutex<Option<mpsc::Sender<Message>>>>,
    thread: Mutex<Option<JoinHandle<()>>>,
}

impl StoreActor {
    pub(crate) async fn open(path: PathBuf) -> Result<Self, String> {
        let (sender, mut receiver) = mpsc::channel(STORE_QUEUE_CAPACITY);
        let (ready_tx, ready_rx) = std::sync::mpsc::sync_channel(1);
        let thread = std::thread::Builder::new()
            .name("talosd-sqlite".into())
            .spawn(move || {
                let mut store = match SqliteStore::open(path) {
                    Ok(store) => {
                        let _ = ready_tx.send(Ok(()));
                        store
                    }
                    Err(error) => {
                        let _ = ready_tx.send(Err(error.to_string()));
                        return;
                    }
                };
                while let Some(message) = receiver.blocking_recv() {
                    match message {
                        Message::Run(task) => task(&mut store),
                        Message::Shutdown => break,
                    }
                }
            })
            .map_err(|error| error.to_string())?;
        let ready = tokio::task::spawn_blocking(move || ready_rx.recv())
            .await
            .map_err(|error| error.to_string())?
            .map_err(|error| error.to_string())?;
        if let Err(error) = ready {
            let _ = tokio::task::spawn_blocking(move || thread.join()).await;
            return Err(error);
        }
        Ok(Self {
            sender: Arc::new(Mutex::new(Some(sender))),
            thread: Mutex::new(Some(thread)),
        })
    }

    pub(crate) async fn run<T, F>(&self, operation: F) -> Result<T, String>
    where
        T: Send + 'static,
        F: FnOnce(&mut SqliteStore) -> StoreResult<T> + Send + 'static,
    {
        let (reply, response) = oneshot::channel();
        let task: StoreTask = Box::new(move |store| {
            let result = operation(store).map_err(|error| error.to_string());
            let _ = reply.send(result);
        });
        let sender = self
            .sender
            .lock()
            .map_err(|_| "SQLite sender lock poisoned")?
            .as_ref()
            .cloned()
            .ok_or("SQLite actor is shutting down")?;
        // Bounded backpressure: wait for queue space instead of dropping lifecycle
        // writes when a burst of provider events fills the channel.
        sender
            .send(Message::Run(task))
            .await
            .map_err(|_| "SQLite actor has stopped".to_owned())?;
        response
            .await
            .map_err(|_| "SQLite actor dropped its response".to_owned())?
    }

    pub(crate) async fn shutdown(&self) {
        let sender = self.sender.lock().ok().and_then(|mut sender| sender.take());
        let thread = self.thread.lock().ok().and_then(|mut thread| thread.take());
        let _ = tokio::task::spawn_blocking(move || {
            if let Some(sender) = sender {
                let _ = sender.blocking_send(Message::Shutdown);
            }
            if let Some(thread) = thread {
                let _ = thread.join();
            }
        })
        .await;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn bounded_actor_applies_backpressure_instead_of_dropping_requests() {
        let actor = Arc::new(StoreActor::open(PathBuf::from(":memory:")).await.unwrap());
        let mut tasks = Vec::new();
        for _ in 0..512 {
            let actor = actor.clone();
            tasks.push(tokio::spawn(async move {
                actor.run(|store| store.schema_version()).await
            }));
        }
        for task in tasks {
            assert_eq!(task.await.unwrap().unwrap(), 2);
        }
        actor.shutdown().await;
    }
}
