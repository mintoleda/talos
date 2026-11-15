//! Native `CommonMark` rendering for assistant messages, including partial streams.
//! HTML remains inert text and images show alt text; rendering never fetches URLs.
use std::ops::Range;

use gpui::{
    FontStyle, FontWeight, InteractiveText, IntoElement, SharedString, StrikethroughStyle,
    StyledText, TextRun, UnderlineStyle, div, font, prelude::*, px, rgb,
};
use pulldown_cmark::{CodeBlockKind, Event, Options, Parser, Tag, TagEnd};

use crate::{ACCENT, BORDER, INK, MUTED};

// These are independent, composable inline marks, not mutually exclusive states.
#[allow(clippy::struct_excessive_bools)]
#[derive(Clone, Debug, Default, PartialEq, Eq)]
struct InlineStyle {
    strong: bool,
    emphasis: bool,
    strike: bool,
    code: bool,
    link: Option<String>,
}

#[derive(Debug)]
struct Span {
    text: String,
    style: InlineStyle,
}

#[derive(Debug, Default, PartialEq, Eq)]
enum Kind {
    #[default]
    Paragraph,
    Heading(u8),
    Code(String),
    Rule,
}

#[derive(Debug, Default)]
struct Block {
    kind: Kind,
    spans: Vec<Span>,
    depth: usize,
    quote: bool,
    marker: String,
}

impl Block {
    fn push(&mut self, text: &str, style: &InlineStyle) {
        if let Some(last) = self.spans.last_mut()
            && last.style == *style
        {
            last.text.push_str(text);
        } else if !text.is_empty() {
            self.spans.push(Span {
                text: text.into(),
                style: style.clone(),
            });
        }
    }

    fn finish(&mut self, blocks: &mut Vec<Self>) {
        let next = Self {
            depth: self.depth,
            quote: self.quote,
            ..Self::default()
        };
        let block = std::mem::replace(self, next);
        if !block.spans.is_empty() || block.kind == Kind::Rule {
            blocks.push(block);
        }
    }
}

#[allow(clippy::too_many_lines)]
fn parse(source: &str) -> Vec<Block> {
    let mut blocks = Vec::new();
    let mut block = Block::default();
    let mut style = InlineStyle::default();
    let mut styles = Vec::new();
    let mut lists = Vec::<Option<u64>>::new();
    let mut quotes = 0_usize;
    for event in Parser::new_ext(
        source,
        Options::ENABLE_STRIKETHROUGH | Options::ENABLE_TASKLISTS,
    ) {
        match event {
            Event::Start(tag) => match tag {
                Tag::Heading { level, .. } => {
                    if !block.spans.is_empty() {
                        block.finish(&mut blocks);
                    }
                    block.kind = Kind::Heading(level as u8);
                }
                Tag::CodeBlock(kind) => {
                    // Retain an enclosing list marker for a code-only item.
                    if !block.spans.is_empty() {
                        block.finish(&mut blocks);
                    }
                    block.kind = Kind::Code(match kind {
                        CodeBlockKind::Fenced(language) => language.into_string(),
                        CodeBlockKind::Indented => String::new(),
                    });
                }
                Tag::List(start) => {
                    block.finish(&mut blocks);
                    lists.push(start);
                    block.depth = lists.len();
                }
                Tag::Item => {
                    block.finish(&mut blocks);
                    block.marker = match lists.last_mut() {
                        Some(Some(number)) => {
                            let marker = format!("{number}.");
                            *number = number.saturating_add(1);
                            marker
                        }
                        _ => "•".into(),
                    };
                }
                Tag::BlockQuote(_) => {
                    block.finish(&mut blocks);
                    quotes += 1;
                    block.quote = true;
                }
                Tag::Strong | Tag::Emphasis | Tag::Strikethrough | Tag::Link { .. } => {
                    styles.push(style.clone());
                    match tag {
                        Tag::Strong => style.strong = true,
                        Tag::Emphasis => style.emphasis = true,
                        Tag::Strikethrough => style.strike = true,
                        Tag::Link { dest_url, .. } => style.link = Some(dest_url.into_string()),
                        _ => unreachable!(),
                    }
                }
                _ => {}
            },
            Event::End(tag) => match tag {
                TagEnd::Paragraph
                | TagEnd::Heading(_)
                | TagEnd::CodeBlock
                | TagEnd::Item
                | TagEnd::HtmlBlock => {
                    block.finish(&mut blocks);
                }
                TagEnd::List(_) => {
                    block.finish(&mut blocks);
                    lists.pop();
                    block.depth = lists.len();
                }
                TagEnd::BlockQuote(_) => {
                    block.finish(&mut blocks);
                    quotes = quotes.saturating_sub(1);
                    block.quote = quotes > 0;
                }
                TagEnd::Strong | TagEnd::Emphasis | TagEnd::Strikethrough | TagEnd::Link => {
                    style = styles.pop().unwrap_or_default();
                }
                _ => {}
            },
            Event::Text(text) | Event::Html(text) | Event::InlineHtml(text) => {
                block.push(&text, &style);
            }
            Event::Code(text) => {
                block.push(
                    &text,
                    &InlineStyle {
                        code: true,
                        ..style.clone()
                    },
                );
            }
            Event::SoftBreak => block.push(" ", &style),
            Event::HardBreak => block.push("\n", &style),
            Event::Rule => {
                block.finish(&mut blocks);
                block.kind = Kind::Rule;
                block.finish(&mut blocks);
            }
            Event::TaskListMarker(checked) => block.push(if checked { "☑ " } else { "☐ " }, &style),
            _ => {}
        }
    }
    block.finish(&mut blocks);
    blocks
}

// Do not launch local files, custom schemes, or model-supplied commands.
fn safe_link(url: &str) -> bool {
    let url = url.to_ascii_lowercase();
    url.starts_with("https://") || url.starts_with("http://") || url.starts_with("mailto:")
}

type Links = Vec<(Range<usize>, String)>;

fn text_runs(block: &Block) -> (String, Vec<TextRun>, Links) {
    let mut text = String::new();
    let mut runs = Vec::new();
    let mut links = Vec::new();
    for span in &block.spans {
        let start = text.len();
        text.push_str(&span.text);
        let code = span.style.code || matches!(block.kind, Kind::Code(_));
        let mut font = font(if code { "JetBrains Mono" } else { "Inter" });
        if span.style.strong || matches!(block.kind, Kind::Heading(_)) {
            font.weight = FontWeight::BOLD;
        }
        if span.style.emphasis {
            font.style = FontStyle::Italic;
        }
        let link = span.style.link.as_ref().filter(|url| safe_link(url));
        if let Some(url) = link {
            links.push((start..text.len(), url.clone()));
        }
        runs.push(TextRun {
            len: span.text.len(),
            font,
            color: rgb(if link.is_some() { ACCENT } else { INK }).into(),
            background_color: span.style.code.then(|| rgb(0x202720).into()),
            underline: link.map(|_| UnderlineStyle {
                thickness: px(1.),
                ..Default::default()
            }),
            strikethrough: span.style.strike.then(|| StrikethroughStyle {
                thickness: px(1.),
                ..Default::default()
            }),
        });
    }
    (text, runs, links)
}

pub struct Document {
    blocks: Vec<Block>,
}

impl Document {
    pub fn new(source: &str) -> Self {
        Self {
            blocks: parse(source),
        }
    }

    pub fn render(&self, id: &str) -> impl IntoElement {
        render_blocks(id, &self.blocks)
    }
}

#[cfg(test)]
pub fn render(id: &str, source: &str) -> impl IntoElement {
    Document::new(source).render(id).into_any_element()
}

fn render_blocks(id: &str, blocks: &[Block]) -> impl IntoElement {
    let mut document = div()
        .w_full()
        .min_w_0()
        .flex_shrink_0()
        .flex()
        .flex_col()
        .gap(px(12.));
    for (index, block) in blocks.iter().enumerate() {
        let mut row = div().w_full().min_w_0().flex().gap(px(8.));
        // Limit visual nesting without truncating any content.
        for _ in 1..block.depth.min(16) {
            row = row.child(div().w(px(16.)).flex_shrink_0());
        }
        if block.depth > 0 {
            row = row.child(
                div()
                    .w(px(24.))
                    .flex_shrink_0()
                    .text_color(rgb(MUTED))
                    .child(block.marker.clone()),
            );
        }
        let mut body = div()
            .flex_1()
            .min_w_0()
            .text_size(px(14.))
            .line_height(px(23.));
        if block.quote {
            body = body.border_l_2().border_color(rgb(BORDER)).pl(px(12.));
        }
        match &block.kind {
            Kind::Heading(level) => {
                let size = match level {
                    1 => 24.,
                    2 => 20.,
                    3 => 17.,
                    _ => 14.,
                };
                body = body.text_size(px(size)).line_height(px(size + 9.));
            }
            Kind::Code(language) => {
                body = body
                    .bg(rgb(0x111712))
                    .border_1()
                    .border_color(rgb(BORDER))
                    .rounded(px(10.))
                    .p(px(12.))
                    .text_size(px(13.));
                if !language.is_empty() {
                    body = body.child(
                        div()
                            .text_size(px(11.))
                            .text_color(rgb(MUTED))
                            .mb(px(6.))
                            .child(language.clone()),
                    );
                }
            }
            Kind::Rule => {
                document = document.child(row.child(body.h(px(1.)).bg(rgb(BORDER))));
                continue;
            }
            Kind::Paragraph => {}
        }
        let (text, runs, links) = text_runs(block);
        let ranges = links.iter().map(|(range, _)| range.clone()).collect();
        let text = InteractiveText::new(
            SharedString::from(format!("markdown-{id}-{index}")),
            StyledText::new(text).with_runs(runs),
        )
        .on_click(ranges, move |index, _, cx| cx.open_url(&links[index].1));
        document = document.child(row.child(body.child(text)));
    }
    document
}

#[cfg(test)]
mod tests {
    use super::*;

    struct MarkdownView {
        source: String,
        scroll: gpui::ScrollHandle,
    }

    impl gpui::Render for MarkdownView {
        fn render(
            &mut self,
            _: &mut gpui::Window,
            _: &mut gpui::Context<Self>,
        ) -> impl IntoElement {
            div().size_full().overflow_hidden().child(
                div()
                    .id("viewport")
                    .size_full()
                    .overflow_y_scroll()
                    .track_scroll(&self.scroll)
                    .child(
                        div()
                            .debug_selector(|| "markdown-document".into())
                            .w_full()
                            .child(render("reply", &self.source)),
                    ),
            )
        }
    }

    #[gpui::test]
    fn markdown_wraps_scrolls_and_reflows_as_the_response_streams(cx: &mut gpui::TestAppContext) {
        let scroll = gpui::ScrollHandle::new();
        let window = cx.open_window(gpui::size(px(480.), px(320.)), |_, _| MarkdownView {
            source: "A **short** response.".into(),
            scroll: scroll.clone(),
        });
        cx.run_until_parked();
        let cx = gpui::VisualTestContext::from_window(*window, cx).into_mut();
        let bounds = cx.debug_bounds("markdown-document").unwrap();
        assert!(bounds.size.height > px(0.) && bounds.size.height < px(50.));
        assert_eq!(scroll.max_offset().y, px(0.));
        window.update(cx, |view, _, cx| {
            view.source = format!("# Tools\n\n{}\n```rust\n{}\n", "- **Explore:** read `src/` and inspect files with a long explanation that should wrap in the reading column.\n".repeat(12), "    let x = 1;\n".repeat(12));
            cx.notify();
        }).unwrap();
        cx.run_until_parked();
        let bounds = cx.debug_bounds("markdown-document").unwrap();
        assert!(
            bounds.size.height > px(800.),
            "Markdown must keep its natural height: {bounds:?}"
        );
        assert!(bounds.size.width <= px(480.));
        assert!(scroll.max_offset().y > px(480.));
        assert_eq!(scroll.max_offset().x, px(0.));
    }

    fn plain(block: &Block) -> String {
        block.spans.iter().map(|span| span.text.as_str()).collect()
    }

    #[test]
    fn screenshot_reply_renders_lists_bold_and_inline_code() {
        let blocks = parse(
            "I can use tools to:\n\n- **Explore files:** list directories.\n- **Run commands:** use `git status`.\n\nDone.",
        );
        assert_eq!(blocks.len(), 4);
        assert_eq!(blocks[1].marker, "•");
        assert_eq!(plain(&blocks[1]), "Explore files: list directories.");
        assert!(blocks[1].spans[0].style.strong);
        assert!(
            blocks[2]
                .spans
                .iter()
                .any(|span| span.style.code && span.text == "git status")
        );
        assert_eq!(blocks[3].depth, 0);
    }

    #[test]
    fn nested_styles_and_unicode_ranges_are_disjoint_utf8_bytes() {
        let blocks = parse("**bold *é [文](https://example.com)* end** and ~~gone~~");
        let (text, runs, links) = text_runs(&blocks[0]);
        assert_eq!(text, "bold é 文 end and gone");
        assert_eq!(runs.iter().map(|run| run.len).sum::<usize>(), text.len());
        assert_eq!(&text[links[0].0.clone()], "文");
        let chinese = blocks[0]
            .spans
            .iter()
            .find(|span| span.text == "文")
            .unwrap();
        assert!(chinese.style.strong && chinese.style.emphasis);
        assert!(blocks[0].spans.last().unwrap().style.strike);
    }

    #[test]
    fn blocks_preserve_code_numbering_quotes_and_partial_streams() {
        let blocks = parse(
            "# Heading\n\n3. first\n   - nested\n4. second\n\n> quote\n\n---\n\n```rust\nlet x = \"**literal**\";\n",
        );
        assert_eq!(blocks[0].kind, Kind::Heading(1));
        assert_eq!(blocks[1].marker, "3.");
        assert_eq!(blocks[2].depth, 2);
        assert_eq!(blocks[3].marker, "4.");
        assert!(blocks[4].quote);
        assert_eq!(blocks[5].kind, Kind::Rule);
        assert_eq!(blocks[6].kind, Kind::Code("rust".into()));
        assert_eq!(plain(&blocks[6]), "let x = \"**literal**\";\n");
        for source in [
            "",
            "**stream",
            "`partial",
            "[link](",
            "é 🦀",
            "- ",
            "```\npartial",
        ] {
            let _ = parse(source);
        }
    }

    #[test]
    fn untrusted_html_images_and_unsafe_links_are_inert() {
        let source = "[bad](javascript:alert) [file](file:///tmp/a) ![alt](https://remote/image) <script>bad()</script>";
        let blocks = parse(source);
        assert_eq!(text_runs(&blocks[0]).2.len(), 0);
        assert!(plain(&blocks[0]).contains("alt"));
        assert!(plain(&blocks[0]).contains("<script>"));
        assert!(safe_link("HTTPS://example.com"));
        assert!(!safe_link("data:text/html,bad"));
    }
}
