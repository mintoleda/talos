//! Frosted surfaces over app content. Desktop blur remains the compositor's job.
//! Uses the pinned GPUI renderer unchanged: no refraction or pointer animation.
use gpui::{
    AnyElement, App, Bounds, Context, Corners, Element, Global, GlobalElementId, Hsla,
    InspectorElementId, IntoElement, LayoutId, Pixels, Render, Window, div, hsla, prelude::*, px,
    rgb,
};

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum GlassMode {
    #[default]
    Frosted,
    Opaque,
}

#[derive(Clone, Copy, Debug, Default)]
pub struct GlassSettings {
    pub mode: GlassMode,
}
impl Global for GlassSettings {}

impl GlassSettings {
    pub fn from_env() -> Self {
        Self::parse(std::env::var("TALOS_GLASS").ok().as_deref())
    }

    fn parse(mode: Option<&str>) -> Self {
        Self {
            mode: if mode == Some("opaque") {
                GlassMode::Opaque
            } else {
                GlassMode::Frosted
            },
        }
    }

    pub fn wash(self, alpha: f32) -> Hsla {
        // Neutral charcoal with a trace of green. No shifting tint or highlights.
        hsla(
            0.39,
            0.08,
            0.045,
            if self.mode == GlassMode::Opaque {
                1.0
            } else {
                alpha
            },
        )
    }

    pub fn name(self) -> &'static str {
        match self.mode {
            GlassMode::Frosted => "Frosted glass",
            GlassMode::Opaque => "Opaque",
        }
    }

    pub fn set_mode(mode: GlassMode, window: &Window, cx: &mut App) {
        cx.global_mut::<Self>().mode = mode;
        window.set_background_appearance(if mode == GlassMode::Opaque {
            gpui::WindowBackgroundAppearance::Opaque
        } else {
            gpui::WindowBackgroundAppearance::Transparent
        });
    }
}

/// One scene layer: blur first, then translucent fill and sharp controls.
pub fn frosted(corner_radius: f32, blur_radius: f32, child: impl IntoElement) -> Frosted {
    Frosted {
        corner_radius,
        blur_radius,
        child: child.into_any_element(),
    }
}

pub struct Frosted {
    corner_radius: f32,
    blur_radius: f32,
    child: AnyElement,
}

impl Element for Frosted {
    type RequestLayoutState = ();
    type PrepaintState = ();

    fn id(&self) -> Option<gpui::ElementId> {
        None
    }
    fn source_location(&self) -> Option<&'static core::panic::Location<'static>> {
        None
    }
    fn request_layout(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        window: &mut Window,
        cx: &mut App,
    ) -> (LayoutId, ()) {
        (self.child.request_layout(window, cx), ())
    }
    fn prepaint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        _bounds: Bounds<Pixels>,
        _request_layout: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        self.child.prepaint(window, cx);
    }
    fn paint(
        &mut self,
        _id: Option<&GlobalElementId>,
        _inspector_id: Option<&InspectorElementId>,
        bounds: Bounds<Pixels>,
        _request_layout: &mut (),
        _prepaint: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        let settings = *cx.global::<GlassSettings>();
        window.paint_layer(bounds, |window| {
            if settings.mode == GlassMode::Frosted {
                window.paint_backdrop_blur(
                    bounds,
                    Corners::all(px(self.corner_radius)),
                    px(self.blur_radius),
                );
            }
            self.child.paint(window, cx);
        });
    }
}
impl IntoElement for Frosted {
    type Element = Self;
    fn into_element(self) -> Self {
        self
    }
}

/// Backend-free inspection scene. It never connects to or starts a daemon.
pub struct GlassDemo;
impl Render for GlassDemo {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let settings = *cx.global::<GlassSettings>();
        let width = f32::from(window.viewport_size().width);
        let card_width = (width - 80.).clamp(360., 620.);
        let card_left = (width - card_width) * 0.5;
        let mut background = div()
            .size_full()
            .flex()
            .flex_col()
            .items_center()
            .gap(px(12.))
            .p(px(36.));
        for row in 0_u16..16 {
            let mut line = div().flex().gap(px(12.)).h(px(32.));
            for col in 0_u16..9 {
                line = line.child(
                    div()
                        .w(px(100.))
                        .h_full()
                        .rounded(px(4.))
                        .bg(hsla(
                            (f32::from(row) * 0.033 + f32::from(col) * 0.08) % 1.,
                            0.32,
                            0.28,
                            1.,
                        ))
                        .flex()
                        .items_center()
                        .px(px(8.))
                        .text_size(px(11.))
                        .text_color(rgb(0xDAE4DE))
                        .child(format!("{:02} / {:02}", row + 1, col + 1)),
                );
            }
            background = background.child(line);
        }
        let mut controls = div().flex().gap(px(8.));
        for (mode, title) in [
            (GlassMode::Frosted, "Frosted glass"),
            (GlassMode::Opaque, "Opaque"),
        ] {
            controls = controls.child(
                div()
                    .id(title)
                    .px(px(14.))
                    .py(px(9.))
                    .rounded(px(10.))
                    .bg(hsla(
                        0.55,
                        0.25,
                        0.55,
                        if settings.mode == mode { 0.32 } else { 0.08 },
                    ))
                    .cursor_pointer()
                    .child(title)
                    .on_click(cx.listener(move |_, _, window, cx| {
                        GlassSettings::set_mode(mode, window, cx);
                        cx.notify();
                    })),
            );
        }
        div().relative().size_full().overflow_hidden()
            .bg(rgb(0x101A18)).text_color(rgb(0xEEF2EF)).font_family("Inter")
            .child(background)
            .child(frosted(28., 18.,
                div().absolute().left(px(card_left)).top(px(126.)).w(px(card_width)).h(px(312.))
                    .rounded(px(28.)).bg(settings.wash(0.30)).border_1()
                    .border_color(hsla(0., 0., 1., 0.12)).p(px(32.))
                    .flex().flex_col().gap(px(18.))
                    .child(div().text_size(px(11.)).text_color(rgb(0xB4BDB7))
                        .child("TALOS  /  FROSTED MATERIAL LAB"))
                    .child(div().text_size(px(30.)).child("Soft backdrop. Clear foreground."))
                    .child(div().text_size(px(14.)).child(
                        "A quiet charcoal wash over a softly blurred field. No lensing, moving highlights, or decorative animation."))
                    .child(controls)
                    .child(div().text_size(px(11.)).text_color(rgb(0xB4BDB7))
                        .child("In-window blur · No daemon · No idle animation")),
            ))
            .child(frosted(18., 14.,
                div().absolute().left(px((card_left + card_width - 150.).min(width - 330.)))
                    .top(px(400.)).w(px(310.)).h(px(120.))
                    .rounded(px(18.)).bg(settings.wash(0.32)).border_1()
                    .border_color(hsla(0., 0., 1., 0.12)).p(px(20.))
                    .flex().flex_col().gap(px(10.))
                    .child("A second layer, still sharp.")
                    .child(div().text_size(px(12.)).child(settings.name())),
            ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn frosted_is_the_default_including_legacy_full_setting() {
        for value in [None, Some("frosted"), Some("full"), Some("unknown")] {
            assert_eq!(GlassSettings::parse(value).mode, GlassMode::Frosted);
        }
        assert_eq!(GlassSettings::default().mode, GlassMode::Frosted);
    }
    #[test]
    fn opaque_disables_transparency_and_frosted_preserves_wash() {
        assert_eq!(GlassSettings::parse(Some("opaque")).mode, GlassMode::Opaque);
        assert_eq!(GlassSettings::parse(Some("opaque")).wash(0.3).a, 1.);
        assert_eq!(GlassSettings::parse(None).wash(0.3).a, 0.3);
    }
}
