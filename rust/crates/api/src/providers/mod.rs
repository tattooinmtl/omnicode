use std::future::Future;
use std::pin::Pin;

use crate::error::ApiError;
use crate::types::{MessageRequest, MessageResponse};

pub mod claw_provider;
pub mod minimax;
pub mod openai_compat;

pub type ProviderFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, ApiError>> + Send + 'a>>;

pub trait Provider {
    type Stream;

    fn send_message<'a>(
        &'a self,
        request: &'a MessageRequest,
    ) -> ProviderFuture<'a, MessageResponse>;

    fn stream_message<'a>(
        &'a self,
        request: &'a MessageRequest,
    ) -> ProviderFuture<'a, Self::Stream>;
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ProviderKind {
    ClawApi,
    Xai,
    OpenAi,
    MiniMax,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ProviderMetadata {
    pub provider: ProviderKind,
    pub auth_env: &'static str,
    pub base_url_env: &'static str,
    pub default_base_url: &'static str,
}

const MODEL_REGISTRY: &[(&str, ProviderMetadata)] = &[
    (
        "opus",
        ProviderMetadata {
            provider: ProviderKind::ClawApi,
            auth_env: "ANTHROPIC_API_KEY",
            base_url_env: "ANTHROPIC_BASE_URL",
            default_base_url: claw_provider::DEFAULT_BASE_URL,
        },
    ),
    (
        "sonnet",
        ProviderMetadata {
            provider: ProviderKind::ClawApi,
            auth_env: "ANTHROPIC_API_KEY",
            base_url_env: "ANTHROPIC_BASE_URL",
            default_base_url: claw_provider::DEFAULT_BASE_URL,
        },
    ),
    (
        "haiku",
        ProviderMetadata {
            provider: ProviderKind::ClawApi,
            auth_env: "ANTHROPIC_API_KEY",
            base_url_env: "ANTHROPIC_BASE_URL",
            default_base_url: claw_provider::DEFAULT_BASE_URL,
        },
    ),
    (
        "claude-opus-4-6",
        ProviderMetadata {
            provider: ProviderKind::ClawApi,
            auth_env: "ANTHROPIC_API_KEY",
            base_url_env: "ANTHROPIC_BASE_URL",
            default_base_url: claw_provider::DEFAULT_BASE_URL,
        },
    ),
    (
        "claude-sonnet-4-6",
        ProviderMetadata {
            provider: ProviderKind::ClawApi,
            auth_env: "ANTHROPIC_API_KEY",
            base_url_env: "ANTHROPIC_BASE_URL",
            default_base_url: claw_provider::DEFAULT_BASE_URL,
        },
    ),
    (
        "claude-haiku-4-5-20251213",
        ProviderMetadata {
            provider: ProviderKind::ClawApi,
            auth_env: "ANTHROPIC_API_KEY",
            base_url_env: "ANTHROPIC_BASE_URL",
            default_base_url: claw_provider::DEFAULT_BASE_URL,
        },
    ),
    (
        "grok",
        ProviderMetadata {
            provider: ProviderKind::Xai,
            auth_env: "XAI_API_KEY",
            base_url_env: "XAI_BASE_URL",
            default_base_url: openai_compat::DEFAULT_XAI_BASE_URL,
        },
    ),
    (
        "grok-3",
        ProviderMetadata {
            provider: ProviderKind::Xai,
            auth_env: "XAI_API_KEY",
            base_url_env: "XAI_BASE_URL",
            default_base_url: openai_compat::DEFAULT_XAI_BASE_URL,
        },
    ),
    (
        "grok-mini",
        ProviderMetadata {
            provider: ProviderKind::Xai,
            auth_env: "XAI_API_KEY",
            base_url_env: "XAI_BASE_URL",
            default_base_url: openai_compat::DEFAULT_XAI_BASE_URL,
        },
    ),
    (
        "grok-3-mini",
        ProviderMetadata {
            provider: ProviderKind::Xai,
            auth_env: "XAI_API_KEY",
            base_url_env: "XAI_BASE_URL",
            default_base_url: openai_compat::DEFAULT_XAI_BASE_URL,
        },
    ),
    (
        "grok-2",
        ProviderMetadata {
            provider: ProviderKind::Xai,
            auth_env: "XAI_API_KEY",
            base_url_env: "XAI_BASE_URL",
            default_base_url: openai_compat::DEFAULT_XAI_BASE_URL,
        },
    ),
    (
        "minimax",
        ProviderMetadata {
            provider: ProviderKind::MiniMax,
            auth_env: "MINIMAX_API_KEY",
            base_url_env: "MINIMAX_BASE_URL",
            default_base_url: minimax::DEFAULT_MINIMAX_BASE_URL,
        },
    ),
    (
        "minimax-chat",
        ProviderMetadata {
            provider: ProviderKind::MiniMax,
            auth_env: "MINIMAX_API_KEY",
            base_url_env: "MINIMAX_BASE_URL",
            default_base_url: minimax::DEFAULT_MINIMAX_BASE_URL,
        },
    ),
    (
        "minimax-vl",
        ProviderMetadata {
            provider: ProviderKind::MiniMax,
            auth_env: "MINIMAX_API_KEY",
            base_url_env: "MINIMAX_BASE_URL",
            default_base_url: minimax::DEFAULT_MINIMAX_BASE_URL,
        },
    ),
];

#[must_use]
pub fn resolve_model_alias(model: &str) -> String {
    let trimmed = model.trim();
    let lower = trimmed.to_ascii_lowercase();
    MODEL_REGISTRY
        .iter()
        .find_map(|(alias, metadata)| {
            (*alias == lower).then_some(match metadata.provider {
                ProviderKind::ClawApi => match *alias {
                    "opus" => "claude-opus-4-6",
                    "sonnet" => "claude-sonnet-4-6",
                    "haiku" => "claude-haiku-4-5-20251213",
                    _ => trimmed,
                },
                ProviderKind::Xai => match *alias {
                    "grok" | "grok-3" => "grok-3",
                    "grok-mini" | "grok-3-mini" => "grok-3-mini",
                    "grok-2" => "grok-2",
                    _ => trimmed,
                },
                ProviderKind::MiniMax => match *alias {
                    "minimax" => "minimax-chat",
                    _ => trimmed,
                },
                ProviderKind::OpenAi => trimmed,
            })
        })
        .map_or_else(|| trimmed.to_string(), ToOwned::to_owned)
}

#[must_use]
pub fn metadata_for_model(model: &str) -> Option<ProviderMetadata> {
    let canonical = resolve_model_alias(model);
    let lower = canonical.to_ascii_lowercase();
    if let Some((_, metadata)) = MODEL_REGISTRY.iter().find(|(alias, _)| *alias == lower) {
        return Some(*metadata);
    }
    if lower.starts_with("grok") {
        return Some(ProviderMetadata {
            provider: ProviderKind::Xai,
            auth_env: "XAI_API_KEY",
            base_url_env: "XAI_BASE_URL",
            default_base_url: openai_compat::DEFAULT_XAI_BASE_URL,
        });
    }
    if lower.starts_with("minimax") {
        return Some(ProviderMetadata {
            provider: ProviderKind::MiniMax,
            auth_env: "MINIMAX_API_KEY",
            base_url_env: "MINIMAX_BASE_URL",
            default_base_url: minimax::DEFAULT_MINIMAX_BASE_URL,
        });
    }
    None
}

/// Chooses the wire protocol for `model`.
///
/// `CLAW_PROVIDER` wins when it names a known protocol (`openai`,
/// `anthropic`, `xai`, `minimax`). OmniCode sets it so the selected endpoint
/// is used even when the model name belongs to another family. Otherwise the
/// model name is matched against the built-in registry, then whichever
/// credential is present in the environment.
#[must_use]
pub fn detect_provider_kind(model: &str) -> ProviderKind {
    let forced = std::env::var("CLAW_PROVIDER").ok();
    if let Some(kind) = forced_provider_from(forced.as_deref()) {
        return kind;
    }
    if let Some(metadata) = metadata_for_model(model) {
        return metadata.provider;
    }
    if claw_provider::has_auth_from_env_or_saved().unwrap_or(false) {
        return ProviderKind::ClawApi;
    }
    if openai_compat::has_api_key("OPENAI_API_KEY") {
        return ProviderKind::OpenAi;
    }
    if openai_compat::has_api_key("XAI_API_KEY") {
        return ProviderKind::Xai;
    }
    if minimax::has_api_key("MINIMAX_API_KEY") {
        return ProviderKind::MiniMax;
    }
    ProviderKind::ClawApi
}

fn forced_provider_from(value: Option<&str>) -> Option<ProviderKind> {
    let raw = value?;
    match raw.trim().to_ascii_lowercase().as_str() {
        "openai" | "openai-compat" | "custom" => Some(ProviderKind::OpenAi),
        "anthropic" | "claw" | "claude" => Some(ProviderKind::ClawApi),
        "xai" | "grok" => Some(ProviderKind::Xai),
        "minimax" => Some(ProviderKind::MiniMax),
        _ => None,
    }
}

#[must_use]
pub fn max_tokens_for_model(model: &str) -> u32 {
    // OmniCode sets this so a small-balance gateway is not billed for a
    // 64k completion. Unset, callers keep the previous ceilings.
    if let Ok(raw) = std::env::var("CLAW_MAX_TOKENS") {
        if let Ok(n) = raw.trim().parse::<u32>() {
            if (16..=64_000).contains(&n) {
                return n;
            }
        }
    }
    let canonical = resolve_model_alias(model);
    if canonical.contains("opus") {
        32_000
    } else {
        64_000
    }
}

#[cfg(test)]
mod tests {
    use super::{
        detect_provider_kind, forced_provider_from, max_tokens_for_model, resolve_model_alias,
        ProviderKind,
    };

    #[test]
    fn resolves_grok_aliases() {
        assert_eq!(resolve_model_alias("grok"), "grok-3");
        assert_eq!(resolve_model_alias("grok-mini"), "grok-3-mini");
        assert_eq!(resolve_model_alias("grok-2"), "grok-2");
    }

    #[test]
    fn resolves_minimax_aliases() {
        assert_eq!(resolve_model_alias("minimax"), "minimax-chat");
        assert_eq!(resolve_model_alias("minimax-chat"), "minimax-chat");
        assert_eq!(resolve_model_alias("minimax-vl"), "minimax-vl");
    }

    #[test]
    fn detects_provider_from_model_name_first() {
        assert_eq!(detect_provider_kind("grok"), ProviderKind::Xai);
        assert_eq!(
            detect_provider_kind("claude-sonnet-4-6"),
            ProviderKind::ClawApi
        );
        assert_eq!(detect_provider_kind("minimax"), ProviderKind::MiniMax);
        assert_eq!(detect_provider_kind("minimax-chat"), ProviderKind::MiniMax);
        assert_eq!(detect_provider_kind("minimax-vl"), ProviderKind::MiniMax);
    }

    #[test]
    fn forced_provider_overrides_model_family() {
        assert_eq!(
            forced_provider_from(Some("openai")),
            Some(ProviderKind::OpenAi)
        );
        assert_eq!(
            forced_provider_from(Some(" anthropic ")),
            Some(ProviderKind::ClawApi)
        );
        assert_eq!(forced_provider_from(Some("xai")), Some(ProviderKind::Xai));
        assert_eq!(
            forced_provider_from(Some("minimax")),
            Some(ProviderKind::MiniMax)
        );
        assert_eq!(forced_provider_from(Some("nope")), None);
        assert_eq!(forced_provider_from(None), None);
    }

    #[test]
    fn keeps_existing_max_token_heuristic() {
        assert_eq!(max_tokens_for_model("opus"), 32_000);
        assert_eq!(max_tokens_for_model("grok-3"), 64_000);
    }
}
