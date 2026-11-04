//! Talos desktop orchestration: provider registry, session lifecycle, persistence, and events.
#![forbid(unsafe_code)]

mod config;
mod provider;
mod service;
mod store_actor;

pub use config::{AppConfig, ConfigError, HomePaths, ProviderConfig, ProviderKind};
pub use provider::{
    AgentProvider, PermissionHandler, ProviderDiagnostic, ProviderError, ProviderEvent,
    ProviderModel, ProviderPermissionDecision, ProviderPermissionOption, ProviderPermissionRequest,
    ProviderPromptResult, ProviderSession, ProviderThinkingOption,
};
pub use service::{
    AppService, CoreDiagnostics, CoreError, CoreResult, CreatedAgent, PermissionOptionSummary,
};
pub use talos_store::SessionSnapshot;
