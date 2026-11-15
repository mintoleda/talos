//! Thinking depth is an advertised provider setting, never a decorative label.
use super::{
    ACCENT, Action, AppView, BORDER, GlassSettings, MUTED, Menu, SECONDARY, frosted, label, state,
};
use gpui::{Context, FontWeight, IntoElement, SharedString, div, hsla, prelude::*, px, rgb};

impl AppView {
    pub(super) fn thinking_control(&self) -> Option<state::ThinkingState> {
        if let Some(session) = &self.selected {
            return self.backend.view.session_thinking(session);
        }
        let model_id = self
            .draft_model
            .as_deref()
            .or(self.configured_model.as_deref())?;
        let model = self
            .backend
            .view
            .provider(self.provider_id())?
            .models
            .iter()
            .find(|model| model.id == model_id)?;
        let mut thinking = model.thinking.clone();
        if thinking.options.is_empty() {
            return None;
        }
        if let Some(current) = self.draft_thinking.get(model_id)
            && thinking.options.iter().any(|(id, _)| id == current)
        {
            thinking.current = Some(current.clone());
        }
        // Do not claim a default if the provider did not advertise one.
        thinking.current = thinking
            .current
            .filter(|current| thinking.options.iter().any(|(id, _)| id == current));
        Some(thinking)
    }

    fn thinking_blocked(&self) -> bool {
        self.running()
            || self.pending.is_some()
            || !self.backend.view.online
            || self.selected.as_deref().is_some_and(|id| {
                self.backend.preferences_pending(id) || !self.backend.ready.contains(id)
            })
    }

    pub(super) fn thinking_level_control(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let blocked = self.thinking_blocked();
        div().when_some(self.thinking_control(), |element, thinking| {
            element.child(
                div()
                    .id("thinking-level")
                    .px(px(4.))
                    .py(px(7.))
                    .rounded(px(8.))
                    .when(blocked, |d| d.opacity(0.5))
                    .when(!blocked, |d| {
                        d.cursor_pointer()
                            .hover(|style| style.bg(hsla(0., 0., 1., 0.07)))
                            .on_click(cx.listener(|this, _, window, cx| {
                                this.toggle_menu(Menu::Thinking, cx);
                                cx.stop_propagation();
                                this.editor
                                    .update(cx, |editor, cx| editor.focus(window, cx));
                            }))
                    })
                    .child(label(
                        thinking.label().to_lowercase(),
                        13.,
                        MUTED,
                        FontWeight::MEDIUM,
                    )),
            )
        })
    }

    pub(super) fn thinking_picker_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let blocked = self.thinking_blocked();
        let mut choices = div().flex().flex_col().gap(px(2.));
        if let Some(thinking) = self.thinking_control() {
            for (id, name) in thinking.options {
                let chosen = thinking.current.as_deref() == Some(id.as_str());
                choices = choices.child(
                    div()
                        .id(SharedString::from(format!("thinking-{id}")))
                        .w_full()
                        .px(px(10.))
                        .py(px(8.))
                        .rounded(px(8.))
                        .flex()
                        .items_center()
                        .justify_between()
                        .when(chosen, |d| d.bg(hsla(0., 0., 1., 0.07)))
                        .when(blocked, |d| d.opacity(0.5))
                        .child(label(
                            name.to_lowercase(),
                            13.,
                            SECONDARY,
                            FontWeight::MEDIUM,
                        ))
                        .child(label(
                            if chosen { "✓" } else { "" },
                            13.,
                            ACCENT,
                            FontWeight::MEDIUM,
                        ))
                        .when(!blocked, |d| {
                            d.cursor_pointer()
                                .hover(|s| s.bg(hsla(0., 0., 1., 0.07)))
                                .on_click(cx.listener(move |this, _, window, cx| {
                                    if this.thinking_blocked() {
                                        return;
                                    }
                                    if let Some(session) = this.selected.clone() {
                                        if !this.backend.submit(Action::Rpc(
                                            talos_ipc::Command::SetThinking {
                                                session_id: session,
                                                thinking_option_id: id.clone(),
                                            },
                                        )) {
                                            return;
                                        }
                                    } else if let Some(model) =
                                        this.draft_model.as_ref().or(this.configured_model.as_ref())
                                    {
                                        this.draft_thinking.insert(model.clone(), id.clone());
                                    }
                                    this.menu = None;
                                    this.editor
                                        .update(cx, |editor, cx| editor.focus(window, cx));
                                    cx.notify();
                                }))
                        }),
                );
            }
        }
        frosted(
            14.,
            18.,
            div()
                .id("thinking-picker")
                .on_click(cx.listener(|_, _, _, cx| cx.stop_propagation()))
                .absolute()
                .bottom_full()
                .mb(px(8.))
                .right(px(44.))
                .w(px(190.))
                .max_w_full()
                .p(px(8.))
                .rounded(px(14.))
                .bg(cx.global::<GlassSettings>().wash(0.96))
                .border_1()
                .border_color(rgb(BORDER))
                .shadow_lg()
                .flex()
                .flex_col()
                .gap(px(5.))
                .child(div().px(px(10.)).py(px(6.)).child(label(
                    "Thinking level",
                    11.,
                    MUTED,
                    FontWeight::MEDIUM,
                )))
                .child(choices),
        )
    }
}
