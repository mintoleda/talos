//! Searchable, provider-grouped model browser. Only real backend options are shown.
use super::*;

impl AppView {
    pub(super) fn model_picker_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        // ACP backends supply a catalogue; model IDs identify inference providers.
        let backend_id = self
            .picker_provider
            .as_deref()
            .unwrap_or_else(|| self.provider_id());
        let backend = self.backend.view.provider(backend_id);
        let session = self.session();
        let groups = backend.map_or_else(Vec::new, |p| model_groups(&p.models, backend_id));
        let current_model = session.and_then(|s| s.model.as_deref()).or_else(|| {
            session
                .is_none()
                .then_some(self.draft_model.as_deref())
                .flatten()
        });
        let current_group = current_model.map(|id| workflow::model_provider(id, backend_id));
        let active = self
            .picker_group
            .as_deref()
            .and_then(|key| groups.iter().find(|g| g.provider.key == key))
            .or_else(|| {
                current_group
                    .as_ref()
                    .and_then(|p| groups.iter().find(|g| g.provider.key == p.key))
            })
            .or_else(|| groups.first());
        let query = self.model_search.read(cx).value().to_string();
        let blocked = self.running()
            || self.pending.is_some()
            || session.is_some_and(|s| self.backend.setting_models.contains(&s.id));
        let mut providers = div()
            .id("model-providers")
            .w(px(48.))
            .flex_shrink_0()
            .h_full()
            .overflow_y_scroll()
            .p(px(7.))
            .flex()
            .flex_col()
            .gap(px(5.))
            .bg(rgb(0x0D100E))
            .border_r_1()
            .border_color(rgb(BORDER));
        for group in &groups {
            let id = group.provider.key.clone();
            let name = group.provider.name.clone();
            let selected = active.is_some_and(|g| g.provider.key == id);
            providers = providers.child(
                div()
                    .id(SharedString::from(format!("picker-provider-{id}")))
                    .w_full()
                    .h(px(38.))
                    .flex_shrink_0()
                    .rounded(px(7.))
                    .flex()
                    .items_center()
                    .justify_center()
                    .when(selected, |d| d.bg(hsla(0., 0., 1., 0.09)))
                    .hover(|s| s.bg(hsla(0., 0., 1., 0.075)))
                    .cursor_pointer()
                    .child(icon(
                        group.provider.icon,
                        24.,
                        if selected { INK } else { MUTED },
                    ))
                    .tooltip(move |_, cx| cx.new(|_| ProviderTooltip(name.clone())).into())
                    .on_click(cx.listener(move |this, _, _, cx| {
                        this.picker_group = Some(id.clone());
                        this.picker_show_all = false;
                        cx.notify();
                    })),
            );
        }
        let models: Vec<_> = active
            .into_iter()
            .flat_map(|g| g.models.iter().copied())
            .filter(|m| {
                !matches!(
                    m.id.to_ascii_lowercase().as_str(),
                    "deepseek/deepseek-chat" | "deepseek/chat"
                )
            })
            .filter(|m| workflow::matches_model_query(&query, &m.label, &m.id))
            .collect();
        let total = active.map_or(0, |g| {
            g.models
                .iter()
                .filter(|model| {
                    !matches!(
                        model.id.to_ascii_lowercase().as_str(),
                        "deepseek/deepseek-chat" | "deepseek/chat"
                    )
                })
                .count()
        });
        let noun = if total == 1 { "model" } else { "models" };
        let reveal_all = self.picker_show_all || !query.trim().is_empty();
        let visible_count = if reveal_all {
            models.len()
        } else {
            models.len().min(4)
        };
        let count = if query.trim().is_empty() {
            format!("{total} {noun}")
        } else {
            format!("{} / {total} {noun}", models.len())
        };
        let mut list = div()
            .id("picker-model-list")
            .flex_1()
            .min_h_0()
            .w_full()
            .overflow_y_scroll()
            .px(px(6.))
            .pb(px(8.))
            .flex()
            .flex_col()
            .gap(px(1.));
        if let Some(provider) = backend {
            if !provider.available {
                list = list.child(picker_message(
                    provider
                        .message
                        .as_deref()
                        .unwrap_or("Provider unavailable."),
                ));
            } else if self.backend.model_catalog_loading.contains(backend_id) {
                list = list.child(picker_message("Loading model catalogue…"));
            } else if models.is_empty() {
                if let Some(error) = self.backend.model_catalog_errors.get(backend_id) {
                    list = list.child(picker_message(error));
                } else {
                    list = list.child(picker_message(if total == 0 {
                        "No models advertised by this provider."
                    } else {
                        "No matching models. Try another name or model ID."
                    }));
                }
            } else {
                let model_icon = active.map_or("icons/sparkles.svg", |group| {
                    if group.provider.key == "anthropic" {
                        "icons/claude-mark.svg"
                    } else {
                        group.provider.icon
                    }
                });
                let model_icon_color = if active.is_some_and(|g| g.provider.key == "anthropic") {
                    ORANGE
                } else {
                    SECONDARY
                };
                for model in models.iter().take(visible_count).copied() {
                    let id = model.id.clone();
                    let tooltip_id = id.clone();
                    let session_id = session.map(|session| session.id.clone());
                    let chosen = current_model == Some(model.id.as_str());
                    let (name, _) = model_picker_label(&model.label, Some(&model.id));
                    list = list.child(
                        div()
                            .id(SharedString::from(format!("picker-model-{id}")))
                            .w_full()
                            .min_w_0()
                            .h(px(48.))
                            .flex_shrink_0()
                            .px(px(12.))
                            .rounded(px(7.))
                            .flex()
                            .items_center()
                            .gap(px(9.))
                            .cursor_pointer()
                            .when(chosen, |d| d.bg(hsla(0., 0., 1., 0.075)))
                            .when(blocked, |d| d.opacity(0.5))
                            .hover(|s| s.bg(hsla(0., 0., 1., 0.065)))
                            .child(div().w(px(28.)).flex_shrink_0().child(icon(
                                model_icon,
                                24.,
                                model_icon_color,
                            )))
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .font_weight(FontWeight::MEDIUM)
                                    .text_size(px(16.))
                                    .text_color(rgb(INK))
                                    .text_ellipsis()
                                    .child(name),
                            )
                            .child(
                                picker_text(if chosen { "✓" } else { "" }, 16., INK)
                                    .w(px(18.))
                                    .flex_shrink_0(),
                            )
                            .tooltip(move |_, cx| {
                                cx.new(|_| ProviderTooltip(tooltip_id.clone())).into()
                            })
                            .on_click(cx.listener(move |this, _, window, cx| {
                                if this.running()
                                    || this.pending.is_some()
                                    || session_id.as_ref().is_some_and(|session| {
                                        this.backend.setting_models.contains(session)
                                    })
                                {
                                    return;
                                }
                                if let Some(session_id) = session_id.as_ref() {
                                    if this.backend.submit(Action::SetModel {
                                        session: session_id.clone(),
                                        model: id.clone(),
                                    }) {
                                        this.menu = None;
                                        this.editor
                                            .update(cx, |editor, cx| editor.focus(window, cx));
                                    }
                                } else {
                                    this.draft_model = Some(id.clone());
                                    this.menu = None;
                                    this.editor
                                        .update(cx, |editor, cx| editor.focus(window, cx));
                                }
                                cx.notify();
                            })),
                    );
                }
                if models.len() > visible_count {
                    let expanded = self.picker_show_all;
                    list = list.child(
                        div()
                            .id("picker-show-more")
                            .w_full()
                            .h(px(44.))
                            .flex_shrink_0()
                            .pl(px(51.))
                            .flex()
                            .items_center()
                            .rounded(px(7.))
                            .cursor_pointer()
                            .hover(|s| s.bg(hsla(0., 0., 1., 0.055)))
                            .child(label(
                                if expanded { "Show less" } else { "Show more" },
                                14.,
                                SECONDARY,
                                FontWeight::MEDIUM,
                            ))
                            .on_click(cx.listener(|this, _, _, cx| {
                                this.picker_show_all = !this.picker_show_all;
                                cx.notify();
                            })),
                    );
                }
            }
        } else {
            list = list.child(picker_message("Waiting for configured providers…"));
        }
        let body = div()
            .w_full()
            .h(px(workflow::model_picker_body_height(
                self.viewport_height,
                self.selected.is_none(),
            )))
            .flex()
            .min_h_0()
            .child(providers)
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .h_full()
                    .flex()
                    .flex_col()
                    .child(
                        div()
                            .h(px(48.))
                            .flex_shrink_0()
                            .px(px(16.))
                            .flex()
                            .items_center()
                            .gap(px(10.))
                            .when_some(active, |d, group| {
                                let name = group.provider.name.clone();
                                d.child(
                                    div()
                                        .id("active-model-provider")
                                        .child(icon(group.provider.icon, 22., INK))
                                        .tooltip(move |_, cx| {
                                            cx.new(|_| ProviderTooltip(name.clone())).into()
                                        }),
                                )
                            })
                            .child(picker_text(count, 12., MUTED)),
                    )
                    .child(list),
            );
        frosted(
            14.,
            18.,
            div()
                .id("model-picker-panel")
                .absolute()
                .bottom_full()
                .mb(px(8.))
                .right_0()
                .w(px(390.))
                .max_w_full()
                .rounded(px(14.))
                .bg(hsla(
                    0.,
                    0.,
                    0.075,
                    if cx.global::<GlassSettings>().mode == GlassMode::Opaque {
                        1.
                    } else {
                        0.96
                    },
                ))
                .border_1()
                .border_color(rgb(BORDER))
                .shadow_lg()
                .flex()
                .flex_col()
                .on_key_down(cx.listener(|this, event: &gpui::KeyDownEvent, window, cx| {
                    if event.keystroke.key == "escape" {
                        this.menu = None;
                        this.editor
                            .update(cx, |editor, cx| editor.focus(window, cx));
                        cx.stop_propagation();
                        cx.notify();
                    }
                }))
                .child(
                    div()
                        .h(px(48.))
                        .w_full()
                        .px(px(15.))
                        .flex()
                        .items_center()
                        .gap(px(10.))
                        .border_b_1()
                        .border_color(hsla(0., 0., 1., 0.055))
                        .child(picker_text("⌕", 22., MUTED).flex_shrink_0())
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .text_color(rgb(INK))
                                .text_size(px(15.))
                                .child(Textarea::new(&self.model_search)),
                        ),
                )
                .child(body),
        )
    }
}

struct ProviderGroup<'a> {
    provider: workflow::ModelProvider,
    models: Vec<&'a state::ModelSummary>,
}

fn model_groups<'a>(models: &'a [state::ModelSummary], backend: &str) -> Vec<ProviderGroup<'a>> {
    let mut groups = std::collections::BTreeMap::new();
    for model in models {
        let provider = workflow::model_provider(&model.id, backend);
        groups
            .entry(provider.key.clone())
            .or_insert_with(|| ProviderGroup {
                provider,
                models: Vec::new(),
            })
            .models
            .push(model);
    }
    groups.into_values().collect()
}

struct ProviderTooltip(String);
impl Render for ProviderTooltip {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div()
            .px(px(10.))
            .py(px(6.))
            .rounded(px(6.))
            .bg(rgb(0x303633))
            .border_1()
            .border_color(rgb(BORDER))
            .child(picker_text(self.0.clone(), 12., INK))
    }
}

fn picker_text(text: impl Into<SharedString>, size: f32, color: u32) -> gpui::Div {
    div()
        .min_w_0()
        .font_family("monospace")
        .text_size(px(size))
        .text_color(rgb(color))
        .text_ellipsis()
        .child(text.into())
}
fn picker_message(text: &str) -> gpui::Div {
    div()
        .px(px(10.))
        .py(px(12.))
        .text_size(px(12.))
        .text_color(rgb(MUTED))
        .child(text.to_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn groups_catalogue_by_inference_provider_without_changing_routing_ids() {
        let models: Vec<_> = [
            "openai/gpt-sol",
            "openai-codex/gpt-sol",
            "deepseek/chat",
            "openai/gpt-luna",
        ]
        .into_iter()
        .map(|id| state::ModelSummary {
            id: id.into(),
            label: id.into(),
            default: false,
        })
        .collect();
        let groups = model_groups(&models, "talos");
        assert_eq!(groups.len(), 2);
        assert!(
            groups
                .iter()
                .all(|g| g.provider.key != "talos" && g.provider.key != "openai")
        );
        let codex = groups
            .iter()
            .find(|g| g.provider.key == "openai-codex")
            .unwrap();
        assert_eq!(codex.provider.name, "Codex");
        assert_eq!(codex.provider.icon, "icons/openai.svg");
        assert_eq!(codex.models.len(), 3);
        assert_eq!(codex.models[0].id, "openai/gpt-sol");
        assert_eq!(codex.models[1].id, "openai-codex/gpt-sol");
        assert!(model_groups(&[], "talos").is_empty());
    }
}
