//! Provider ports and ACP adapters used by application orchestration.

use std::{
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use talos_acp::{
    AcpClient, AcpError, AcpEvent, ConfigOption, PermissionDecision, PermissionFuture,
    PermissionHandler as AcpPermissionHandler, PermissionRequest as AcpPermissionRequest,
    ProcessConfig, SessionLoadState, SessionState,
};
use talos_domain::{Capabilities, ModelRef, ProviderDescriptor, ProviderId, ProviderMode};
use thiserror::Error;
use tokio_util::sync::CancellationToken;

use crate::config::{ProviderConfig, ProviderKind};

#[derive(Debug, Error)]
pub enum ProviderError {
    #[error(transparent)]
    Acp(#[from] AcpError),
    #[error("provider {provider} does not support {feature}")]
    Unsupported {
        provider: String,
        feature: &'static str,
    },
    #[error("provider error: {0}")]
    Other(String),
    #[error("{feature} option {option:?} is not advertised by provider {provider}")]
    InvalidOption {
        provider: String,
        feature: &'static str,
        option: String,
    },
}

#[derive(Clone, Debug)]
pub struct ProviderDiagnostic {
    pub descriptor: ProviderDescriptor,
    pub command: PathBuf,
    pub executable_found: bool,
    pub message: Option<String>,
}

#[derive(Clone, Debug)]
pub struct ProviderPermissionOption {
    pub id: String,
    pub label: String,
    pub kind: Option<String>,
}

#[derive(Clone, Debug)]
pub struct ProviderPermissionRequest {
    pub native_session_id: String,
    pub title: String,
    pub kind: Option<String>,
    pub tool_call_id: String,
    pub raw_input: Option<Value>,
    pub locations: Vec<(String, Option<u64>)>,
    pub options: Vec<ProviderPermissionOption>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum ProviderPermissionDecision {
    Selected(String),
    Cancelled,
}

#[async_trait]
pub trait PermissionHandler: Send + Sync {
    async fn request_permission(
        &self,
        request: ProviderPermissionRequest,
        cancellation: CancellationToken,
    ) -> ProviderPermissionDecision;
}

#[derive(Clone, Debug)]
pub enum ProviderEvent {
    Acp(AcpEvent),
}

#[derive(Clone, Debug)]
pub struct ProviderPromptResult {
    pub stop_reason: String,
    pub usage: Option<talos_acp::PromptUsage>,
}

/// A provider model with enough metadata for the UI to render a picker.
#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct ProviderModel {
    pub id: String,
    pub label: String,
    pub description: Option<String>,
    pub thinking_options: Vec<ProviderThinkingOption>,
    pub default_thinking_option_id: Option<String>,
    pub available: bool,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct ProviderThinkingOption {
    pub id: String,
    pub label: String,
}

/// Find the session config option that drives thinking depth, if the provider
/// advertises one. Talos currently uses `thought_level`.
pub(crate) fn thinking_config_option(options: &[ConfigOption]) -> Option<&ConfigOption> {
    options.iter().find(|option| {
        option.id.eq_ignore_ascii_case("thought_level")
            || option
                .category
                .as_deref()
                .is_some_and(|category| category.to_ascii_lowercase().contains("thought"))
            || option.name.to_ascii_lowercase().contains("thinking")
    })
}

#[async_trait]
pub trait ProviderSession: Send + Sync {
    fn provider_id(&self) -> &ProviderId;
    fn native_session_id(&self) -> &str;
    fn capabilities(&self) -> Capabilities;
    fn modes(&self) -> Vec<ProviderMode>;
    fn models(&self) -> Vec<ModelRef>;
    fn config_options(&self) -> Vec<ConfigOption>;
    /// Rich model catalogue derived from [`Self::models`] by default. Providers
    /// that retain labels, descriptions, and thinking options override this.
    fn models_detailed(&self) -> Vec<ProviderModel> {
        self.models()
            .into_iter()
            .map(|model| ProviderModel {
                id: model.model_id.clone(),
                label: model.model_id,
                description: None,
                thinking_options: Vec::new(),
                default_thinking_option_id: None,
                available: true,
            })
            .collect()
    }
    async fn prompt(
        &self,
        text: &str,
        cancellation: &CancellationToken,
    ) -> Result<ProviderPromptResult, ProviderError>;
    async fn cancel(&self) -> Result<(), ProviderError>;
    async fn set_mode(&self, mode_id: &str) -> Result<(), ProviderError>;
    async fn set_model(&self, model_id: &str) -> Result<(), ProviderError>;
    async fn set_config_option(&self, id: &str, value: &str) -> Result<(), ProviderError>;
    async fn next_event(&self) -> Option<ProviderEvent>;
    async fn shutdown(&self, grace: Duration);
}

#[async_trait]
pub trait AgentProvider: Send + Sync {
    fn id(&self) -> &ProviderId;
    async fn diagnostic(&self) -> ProviderDiagnostic;
    async fn create_session(
        &self,
        cwd: &Path,
        permissions: Arc<dyn PermissionHandler>,
    ) -> Result<Box<dyn ProviderSession>, ProviderError>;
    async fn resume_session(
        &self,
        cwd: &Path,
        native_session_id: &str,
        permissions: Arc<dyn PermissionHandler>,
    ) -> Result<Box<dyn ProviderSession>, ProviderError>;
}

pub(crate) fn provider_from_config(
    config: &ProviderConfig,
) -> Result<Arc<dyn AgentProvider>, ProviderError> {
    let id = ProviderId::new(config.id.clone())
        .map_err(|error| ProviderError::Other(error.to_string()))?;
    match config.kind {
        ProviderKind::Acp => Ok(Arc::new(AcpProvider {
            id,
            config: config.clone(),
        })),
        ProviderKind::LegacyPi => Err(ProviderError::Unsupported {
            provider: config.id.clone(),
            feature: "the removed Pi provider",
        }),
    }
}

struct AcpProvider {
    id: ProviderId,
    config: ProviderConfig,
}

#[async_trait]
impl AgentProvider for AcpProvider {
    fn id(&self) -> &ProviderId {
        &self.id
    }

    async fn diagnostic(&self) -> ProviderDiagnostic {
        let found = executable_exists(&self.config.command);
        let descriptor = ProviderDescriptor {
            id: self.id.clone(),
            display_name: self.config.display_name.clone(),
            available: self.config.enabled && found,
            capabilities: Capabilities {
                streaming: true,
                resume: true,
                modes: true,
                tools: true,
                permissions: true,
                images: true,
                usage: true,
                session_persistence: true,
                audio: true,
                model_selection: true,
                config_options: true,
            },
            modes: Vec::new(),
            models: Vec::new(),
        };
        ProviderDiagnostic {
            descriptor,
            command: self.config.command.clone(),
            executable_found: found,
            message: if !self.config.enabled {
                Some("provider is disabled".into())
            } else if !found {
                Some(format!(
                    "executable {:?} was not found",
                    self.config.command
                ))
            } else {
                None
            },
        }
    }

    async fn create_session(
        &self,
        cwd: &Path,
        permissions: Arc<dyn PermissionHandler>,
    ) -> Result<Box<dyn ProviderSession>, ProviderError> {
        let client = self.spawn_client(permissions).await?;
        let state = client.new_session(&cwd.to_string_lossy(), &[]).await?;
        Ok(Box::new(AcpSession::new(
            self.id.clone(),
            client,
            state,
            None,
        )))
    }

    async fn resume_session(
        &self,
        cwd: &Path,
        native_session_id: &str,
        permissions: Arc<dyn PermissionHandler>,
    ) -> Result<Box<dyn ProviderSession>, ProviderError> {
        let client = self.spawn_client(permissions).await?;
        let loaded = client
            .load_session(native_session_id, &cwd.to_string_lossy(), &[])
            .await?;
        Ok(Box::new(AcpSession::new(
            self.id.clone(),
            client,
            loaded,
            Some(native_session_id.to_owned()),
        )))
    }
}

impl AcpProvider {
    async fn spawn_client(
        &self,
        permissions: Arc<dyn PermissionHandler>,
    ) -> Result<AcpClient, ProviderError> {
        let mut process = ProcessConfig::new(&self.config.command);
        process.args = self.config.args.clone();
        process.env = self.config.env.clone();
        process.cwd = self.config.cwd.clone();
        let bridge: Arc<dyn AcpPermissionHandler> = Arc::new(AcpPermissionBridge {
            handler: permissions,
        });
        let client = AcpClient::spawn_with_permission_handler(process, bridge).await?;
        client.initialize().await?;
        Ok(client)
    }
}

struct AcpPermissionBridge {
    handler: Arc<dyn PermissionHandler>,
}

impl AcpPermissionHandler for AcpPermissionBridge {
    fn request_permission(
        &self,
        request: AcpPermissionRequest,
        cancellation: CancellationToken,
    ) -> PermissionFuture<'_> {
        let handler = self.handler.clone();
        Box::pin(async move {
            let permission = ProviderPermissionRequest {
                native_session_id: request.session_id,
                title: request
                    .tool_call
                    .title
                    .unwrap_or_else(|| "Agent tool request".into()),
                kind: request.tool_call.kind,
                tool_call_id: request.tool_call.tool_call_id,
                raw_input: request.tool_call.raw_input,
                locations: request
                    .tool_call
                    .locations
                    .into_iter()
                    .map(|location| (location.path, location.line))
                    .collect(),
                options: request
                    .options
                    .into_iter()
                    .map(|option| ProviderPermissionOption {
                        id: option.option_id,
                        label: option.name,
                        kind: option.kind,
                    })
                    .collect(),
            };
            match handler.request_permission(permission, cancellation).await {
                ProviderPermissionDecision::Selected(option_id) => {
                    PermissionDecision::selected(option_id)
                }
                ProviderPermissionDecision::Cancelled => PermissionDecision::Cancelled,
            }
        })
    }
}

struct AcpSession {
    id: ProviderId,
    client: AcpClient,
    native_id: String,
    capabilities: Capabilities,
    modes: Vec<ProviderMode>,
    models: Vec<ModelRef>,
    model_catalog: Vec<talos_acp::ModelInfo>,
    config_options: Vec<ConfigOption>,
}

impl AcpSession {
    fn new(
        id: ProviderId,
        client: AcpClient,
        state: impl Into<AcpSessionState>,
        native: Option<String>,
    ) -> Self {
        let state = state.into();
        let native_id = native.unwrap_or(state.native_id);
        let session_modes: Vec<ProviderMode> = state
            .modes
            .as_ref()
            .map(|set| {
                set.available_modes
                    .iter()
                    .filter_map(|mode| {
                        ProviderMode::new(mode.id.clone(), mode.name.clone())
                            .ok()
                            .map(|mut value| {
                                value.description.clone_from(&mode.description);
                                value
                            })
                    })
                    .collect()
            })
            .unwrap_or_default();
        let model_catalog: Vec<talos_acp::ModelInfo> = state
            .models
            .as_ref()
            .map(|set| set.available_models.clone())
            .unwrap_or_default();
        let model_refs: Vec<ModelRef> = model_catalog
            .iter()
            .filter_map(|model| ModelRef::new(id.clone(), model.model_id.clone()).ok())
            .collect();
        let client_caps = client
            .capabilities()
            .expect("ACP client is initialized before creating a session");
        let capabilities = Capabilities {
            streaming: true,
            resume: client_caps.agent_capabilities.load_session,
            modes: !session_modes.is_empty(),
            tools: true,
            permissions: true,
            images: client_caps.agent_capabilities.prompt_capabilities.image,
            usage: true,
            session_persistence: client_caps.agent_capabilities.load_session,
            audio: client_caps.agent_capabilities.prompt_capabilities.audio,
            model_selection: !model_refs.is_empty(),
            config_options: !state.config_options.is_empty(),
        };
        Self {
            id,
            client,
            native_id,
            capabilities,
            modes: session_modes,
            models: model_refs,
            model_catalog,
            config_options: state.config_options,
        }
    }
}

struct AcpSessionState {
    native_id: String,
    modes: Option<talos_acp::SessionModes>,
    models: Option<talos_acp::SessionModels>,
    config_options: Vec<ConfigOption>,
}

impl From<SessionState> for AcpSessionState {
    fn from(value: SessionState) -> Self {
        Self {
            native_id: value.session_id,
            modes: value.modes,
            models: value.models,
            config_options: value.config_options,
        }
    }
}
impl From<SessionLoadState> for AcpSessionState {
    fn from(value: SessionLoadState) -> Self {
        Self {
            native_id: String::new(),
            modes: value.modes,
            models: value.models,
            config_options: value.config_options,
        }
    }
}

#[async_trait]
impl ProviderSession for AcpSession {
    fn provider_id(&self) -> &ProviderId {
        &self.id
    }
    fn native_session_id(&self) -> &str {
        &self.native_id
    }
    fn capabilities(&self) -> Capabilities {
        self.capabilities
    }
    fn modes(&self) -> Vec<ProviderMode> {
        self.modes.clone()
    }
    fn models(&self) -> Vec<ModelRef> {
        self.models.clone()
    }
    fn config_options(&self) -> Vec<ConfigOption> {
        self.config_options.clone()
    }

    fn models_detailed(&self) -> Vec<ProviderModel> {
        let thinking = thinking_config_option(&self.config_options);
        let thinking_options: Vec<ProviderThinkingOption> =
            thinking.map_or_else(Vec::new, |option| {
                option
                    .options
                    .iter()
                    .map(|value| ProviderThinkingOption {
                        id: value.value.clone(),
                        label: value.name.clone(),
                    })
                    .collect()
            });
        let default_thinking = thinking
            .map(|option| option.current_value.clone())
            .filter(|value| !value.is_empty());
        self.model_catalog
            .iter()
            .map(|model| ProviderModel {
                id: model.model_id.clone(),
                label: model.name.clone(),
                description: model.description.clone(),
                thinking_options: thinking_options.clone(),
                default_thinking_option_id: default_thinking.clone(),
                available: true,
            })
            .collect()
    }

    async fn prompt(
        &self,
        text: &str,
        cancellation: &CancellationToken,
    ) -> Result<ProviderPromptResult, ProviderError> {
        let block = talos_acp::ContentBlock::Text {
            text: text.to_owned(),
            extra: serde_json::Map::new(),
        };
        let response = self
            .client
            .prompt_cancellable(&self.native_id, &[block], cancellation)
            .await?;
        Ok(ProviderPromptResult {
            stop_reason: response.stop_reason,
            usage: response.usage,
        })
    }
    async fn cancel(&self) -> Result<(), ProviderError> {
        self.client.cancel_session(&self.native_id).await?;
        Ok(())
    }
    async fn set_mode(&self, mode_id: &str) -> Result<(), ProviderError> {
        self.client.set_mode(&self.native_id, mode_id).await?;
        Ok(())
    }
    async fn set_model(&self, model_id: &str) -> Result<(), ProviderError> {
        self.client.set_model(&self.native_id, model_id).await?;
        Ok(())
    }
    async fn set_config_option(&self, id: &str, value: &str) -> Result<(), ProviderError> {
        self.client
            .set_config_option(&self.native_id, id, value)
            .await?;
        Ok(())
    }
    async fn next_event(&self) -> Option<ProviderEvent> {
        self.client.next_event().await.map(ProviderEvent::Acp)
    }
    async fn shutdown(&self, grace: Duration) {
        let deadline = tokio::time::Instant::now() + grace;
        let _ = tokio::time::timeout_at(deadline, self.client.close_session(&self.native_id)).await;
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        let _ = self.client.shutdown(remaining).await;
    }
}

fn executable_exists(command: &Path) -> bool {
    if command.components().count() > 1 || command.is_absolute() {
        return command.is_file();
    }
    std::env::var_os("PATH")
        .is_some_and(|paths| std::env::split_paths(&paths).any(|path| path.join(command).is_file()))
}
