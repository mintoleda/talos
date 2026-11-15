//! Compact session navigation grouped by workspace. Search and sort are local UI state.
use std::{cmp::Ordering, collections::HashMap, path::Path};

use super::{workspace_labels, *};

const SIDEBAR_MIN_WIDTH: f32 = 220.;
const SIDEBAR_MAX_WIDTH: f32 = 480.;
const SIDEBAR_RESIZE_HANDLE_WIDTH: f32 = 8.;
const MAIN_MIN_WIDTH: f32 = 360.;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum SessionSort {
    Recent,
    Oldest,
    Title,
}

impl SessionSort {
    const OPTIONS: [(Self, &'static str); 3] = [
        (Self::Recent, "Recently updated"),
        (Self::Oldest, "Oldest updated"),
        (Self::Title, "Title · A–Z"),
    ];
}

pub(super) fn search_input(window: &mut Window, cx: &mut Context<AppView>) -> Entity<InputState> {
    let input = cx.new(|cx| InputState::new(window, cx).placeholder("Search sessions…"));
    input.update(cx, |state, _| {
        state.set_editor_style(InputEditorStyle {
            foreground: rgb(INK).into(),
            muted_foreground: rgb(MUTED).into(),
            background: hsla(0., 0., 0., 0.),
            border: hsla(0., 0., 0., 0.),
            selection: rgb(ACCENT_WASH).into(),
            caret: rgb(ACCENT).into(),
            ..Default::default()
        });
    });
    input
}

#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum WorkspaceKey<'a> {
    Registered(&'a str),
    Directory(&'a Path),
}

struct SessionRow<'a> {
    session: &'a state::Session,
    workspace: String,
    workspace_key: WorkspaceKey<'a>,
    workspace_path: &'a str,
    branch: Option<&'a str>,
}

fn session_rows<'a>(
    view: &'a state::ViewModel,
    query: &str,
    sort: SessionSort,
) -> Vec<SessionRow<'a>> {
    let terms: Vec<_> = query.split_whitespace().map(str::to_lowercase).collect();
    let labels = workspace_labels::for_paths(view.workspaces.iter().map(|w| w.path.as_str()));
    let workspace_names: HashMap<_, _> = view
        .workspaces
        .iter()
        .zip(labels)
        .map(|(workspace, label)| (workspace.id.as_str(), label))
        .collect();
    let mut rows: Vec<_> = view
        .sessions
        .iter()
        .filter(|s| !s.archived)
        .filter_map(|session| {
            let workspace = view.workspaces.iter().find(|w| w.id == session.workspace);
            let path = workspace.map_or(session.cwd.as_str(), |w| w.path.as_str());
            let name = workspace
                .and_then(|workspace| workspace_names.get(workspace.id.as_str()))
                .map(String::as_str)
                .filter(|name| !name.is_empty())
                .or_else(|| Path::new(path).file_name().and_then(|name| name.to_str()))
                .filter(|name| !name.is_empty())
                .or_else(|| {
                    workspace
                        .map(|w| w.name.as_str())
                        .filter(|name| !name.is_empty())
                })
                .unwrap_or(if path == "/" { "/" } else { "No workspace" });
            let branch = workspace
                .and_then(|w| w.branch.as_deref())
                .map(str::trim)
                .filter(|branch| !branch.is_empty());
            let haystack = format!(
                "{} {} {} {} {}",
                session.title,
                name,
                path,
                session.cwd,
                branch.unwrap_or_default()
            )
            .to_lowercase();
            terms
                .iter()
                .all(|term| haystack.contains(term))
                .then(|| SessionRow {
                    session,
                    workspace: name.to_owned(),
                    workspace_key: workspace.map_or_else(
                        || WorkspaceKey::Directory(Path::new(path)),
                        |w| WorkspaceKey::Registered(&w.id),
                    ),
                    workspace_path: path,
                    branch,
                })
        })
        .collect();
    rows.sort_by(|a, b| {
        let recent = || compare_time(a.session.updated_at, b.session.updated_at, false);
        let order = match sort {
            SessionSort::Recent => recent(),
            SessionSort::Oldest => compare_time(a.session.updated_at, b.session.updated_at, true),
            SessionSort::Title => a
                .session
                .title
                .to_lowercase()
                .cmp(&b.session.title.to_lowercase())
                .then_with(recent),
        };
        // Workspace is always the primary order: session sorts never interleave groups.
        a.workspace
            .to_lowercase()
            .cmp(&b.workspace.to_lowercase())
            .then_with(|| Path::new(a.workspace_path).cmp(Path::new(b.workspace_path)))
            .then_with(|| a.workspace_key.cmp(&b.workspace_key))
            .then(order)
            .then_with(|| a.session.id.cmp(&b.session.id))
    });
    rows
}

fn compare_time(a: Option<i64>, b: Option<i64>, oldest: bool) -> Ordering {
    match (a, b) {
        (Some(a), Some(b)) => {
            if oldest {
                a.cmp(&b)
            } else {
                b.cmp(&a)
            }
        }
        (Some(_), None) => Ordering::Less,
        (None, Some(_)) => Ordering::Greater,
        (None, None) => Ordering::Equal,
    }
}

impl AppView {
    pub(super) fn sidebar(&self, width: f32, cx: &mut Context<Self>) -> impl IntoElement {
        let query = self.session_search.read(cx).value().to_string();
        let rows = session_rows(&self.backend.view, &query, self.session_sort);
        let mut list = div()
            .id("sessions")
            .flex_1()
            .min_h_0()
            .overflow_y_scroll()
            .flex()
            .flex_col()
            .gap(px(2.));
        if rows.is_empty() {
            list = list.child(div().px(px(8.)).py(px(12.)).child(label(
                if !query.trim().is_empty() {
                    "No matching sessions"
                } else if self.backend.view.online {
                    "No sessions yet"
                } else {
                    "Connecting…"
                },
                11.,
                MUTED,
                FontWeight::NORMAL,
            )));
        }
        let mut previous_workspace = None;
        for row in rows {
            if previous_workspace != Some(row.workspace_key) {
                let tooltip = row.workspace_path.to_owned();
                list = list.child(
                    div()
                        .id(SharedString::from(format!(
                            "workspace-heading-{}",
                            row.session.id
                        )))
                        .w_full()
                        .min_w_0()
                        .flex_shrink_0()
                        .px(px(8.))
                        .pt(px(12.))
                        .pb(px(5.))
                        .flex()
                        .items_center()
                        .gap(px(6.))
                        .tooltip(move |_, cx| cx.new(|_| SidebarTooltip(tooltip.clone())).into())
                        .child(icon("icons/folder.svg", 12., MUTED))
                        .child(metadata(row.workspace).flex_1()),
                );
                previous_workspace = Some(row.workspace_key);
            }
            let session = row.session;
            let id = session.id.clone();
            let selected = self.selected.as_ref() == Some(&id);
            let status_color = match session.status {
                SessionStatus::Running => ACCENT,
                SessionStatus::Attention => ORANGE,
                SessionStatus::Error => DANGER,
                _ => MUTED,
            };
            let tooltip = format!(
                "{}\n{}\n{}",
                session.title,
                session.cwd,
                session.status.label()
            );
            list = list.child(
                div()
                    .id(SharedString::from(format!("session-{id}")))
                    .w_full()
                    .min_w_0()
                    .flex_shrink_0()
                    .px(px(8.))
                    .py(px(6.))
                    .rounded(px(6.))
                    .border_1()
                    .border_color(if selected {
                        hsla(0.24, 0.22, 0.56, 0.25)
                    } else {
                        hsla(0., 0., 0., 0.)
                    })
                    .bg(if selected {
                        hsla(0.24, 0.12, 0.18, 0.60)
                    } else {
                        hsla(0., 0., 0., 0.)
                    })
                    .hover(|style| style.bg(hsla(0., 0., 1., 0.055)))
                    .cursor_pointer()
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .tooltip(move |_, cx| cx.new(|_| SidebarTooltip(tooltip.clone())).into())
                    .on_click(cx.listener(move |this, _, window, cx| {
                        this.select(Some(id.clone()), window, cx);
                    }))
                    .child(
                        div()
                            .w_full()
                            .flex()
                            .items_center()
                            .justify_between()
                            .gap(px(8.))
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .font_family("Inter")
                                    .font_weight(FontWeight::MEDIUM)
                                    .text_size(px(13.))
                                    .line_height(px(17.))
                                    .text_color(rgb(INK))
                                    .overflow_hidden()
                                    .whitespace_nowrap()
                                    .text_ellipsis()
                                    .child(session.title.clone()),
                            )
                            .child(
                                div()
                                    .flex_shrink_0()
                                    .flex()
                                    .items_center()
                                    .gap(px(5.))
                                    .when(session.status != SessionStatus::Idle, |d| {
                                        d.child(
                                            div().size(px(4.)).rounded_full().bg(rgb(status_color)),
                                        )
                                    })
                                    .child(metadata(session.time.clone()).flex_shrink_0()),
                            ),
                    )
                    .when_some(row.branch, |d, branch| {
                        d.child(
                            div()
                                .min_w_0()
                                .flex()
                                .items_center()
                                .gap(px(4.))
                                .child(icon("icons/branch.svg", 10., MUTED))
                                .child(metadata(branch.to_owned())),
                        )
                    }),
            );
        }
        div()
            .relative()
            .w(px(width))
            .flex_shrink_0()
            .h_full()
            .min_h_0()
            .flex()
            .flex_col()
            .p(px(8.))
            .gap(px(8.))
            .bg(cx.global::<GlassSettings>().wash(0.30))
            .border_r_1()
            .border_color(hsla(0., 0., 1., 0.07))
            .child(
                div()
                    .w_full()
                    .flex_shrink_0()
                    .flex()
                    .items_center()
                    .gap(px(2.))
                    .child(
                        div()
                            .id("session-search")
                            .on_click(cx.listener(|this, _, window, cx| {
                                this.session_search
                                    .update(cx, |input, cx| input.focus(window, cx));
                            }))
                            .flex_1()
                            .min_w_0()
                            .h(px(30.))
                            .px(px(7.))
                            .rounded(px(6.))
                            .border_1()
                            .border_color(hsla(0., 0., 1., 0.07))
                            .bg(hsla(0., 0., 1., 0.025))
                            .flex()
                            .items_center()
                            .gap(px(5.))
                            .child(icon("icons/search.svg", 12., MUTED))
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .font_family("Inter")
                                    .text_color(rgb(INK))
                                    .text_size(px(11.))
                                    .child(Input::new(&self.session_search)),
                            ),
                    )
                    .child(sidebar_button(
                        "new-conversation",
                        "icons/plus.svg",
                        "New session",
                        cx.listener(|this, _, window, cx| this.select(None, window, cx)),
                    ))
                    .child(sidebar_button(
                        "new-workspace",
                        "icons/folder-plus.svg",
                        "New workspace",
                        cx.listener(|this, _, window, cx| this.pick_workspace(window, cx)),
                    ))
                    .child(sidebar_button(
                        "session-sort",
                        "icons/sort.svg",
                        "Sort sessions within workspaces",
                        cx.listener(|this, _, _, cx| this.toggle_menu(Menu::SessionSort, cx)),
                    )),
            )
            .child(list)
            .child(
                div()
                    .w_full()
                    .flex_shrink_0()
                    .flex()
                    .justify_end()
                    .child(sidebar_button(
                        "appearance",
                        "icons/settings.svg",
                        "Settings",
                        cx.listener(|this, _, _, cx| this.toggle_menu(Menu::Diagnostics, cx)),
                    )),
            )
            .when(self.menu == Some(Menu::SessionSort), |d| {
                d.child(self.session_sort_surface(cx))
            })
    }

    fn session_sort_surface(&self, cx: &mut Context<Self>) -> impl IntoElement {
        let mut options = div().flex().flex_col().gap(px(2.));
        for (sort, title) in SessionSort::OPTIONS {
            let selected = self.session_sort == sort;
            options = options.child(
                div()
                    .id(title)
                    .px(px(9.))
                    .py(px(7.))
                    .rounded(px(5.))
                    .flex()
                    .items_center()
                    .justify_between()
                    .gap(px(8.))
                    .when(selected, |d| d.bg(hsla(0., 0., 1., 0.07)))
                    .hover(|s| s.bg(hsla(0., 0., 1., 0.06)))
                    .cursor_pointer()
                    .child(label(
                        title,
                        11.,
                        if selected { INK } else { SECONDARY },
                        FontWeight::NORMAL,
                    ))
                    .child(label(
                        if selected { "✓" } else { "" },
                        11.,
                        ACCENT,
                        FontWeight::NORMAL,
                    ))
                    .on_click(cx.listener(move |this, _, _, cx| {
                        cx.stop_propagation();
                        this.session_sort = sort;
                        this.menu = None;
                        cx.notify();
                    })),
            );
        }
        frosted(
            8.,
            16.,
            div()
                .id("session-sort-menu")
                .block_mouse_except_scroll()
                .absolute()
                .top(px(44.))
                .right(px(10.))
                .w(px(186.))
                .p(px(5.))
                .rounded(px(8.))
                .border_1()
                .border_color(rgb(BORDER))
                .bg(cx.global::<GlassSettings>().wash(0.96))
                .shadow_lg()
                .child(options),
        )
    }
}

pub(super) fn resize_handle(cx: &mut Context<AppView>) -> impl IntoElement {
    div()
        .id("sidebar-resize-handle")
        .w(px(SIDEBAR_RESIZE_HANDLE_WIDTH))
        .h_full()
        .flex_shrink_0()
        .cursor(gpui::CursorStyle::ResizeLeftRight)
        .hover(|style| style.bg(hsla(0., 0., 1., 0.035)))
        .on_drag(SidebarResizeDrag, |_, _, _, cx| {
            cx.new(|_| SidebarResizeDrag)
        })
        .on_drag_move(cx.listener(
            |this, event: &gpui::DragMoveEvent<SidebarResizeDrag>, window, cx| {
                let viewport_width = f32::from(window.viewport_size().width);
                let next_width =
                    sidebar_width_at(f32::from(event.event.position.x), viewport_width);
                if (this.sidebar_width - next_width).abs() >= 0.5 {
                    this.sidebar_width = next_width;
                    cx.notify();
                }
            },
        ))
        .child(
            div()
                .absolute()
                .top_0()
                .bottom_0()
                .left(px(3.5))
                .w(px(1.))
                .bg(hsla(0., 0., 1., 0.09)),
        )
}

fn sidebar_width_at(pointer_x: f32, viewport_width: f32) -> f32 {
    let max_width = (viewport_width - MAIN_MIN_WIDTH - SIDEBAR_RESIZE_HANDLE_WIDTH)
        .clamp(SIDEBAR_MIN_WIDTH, SIDEBAR_MAX_WIDTH);
    (pointer_x - SIDEBAR_RESIZE_HANDLE_WIDTH / 2.).clamp(SIDEBAR_MIN_WIDTH, max_width)
}

struct SidebarResizeDrag;
impl Render for SidebarResizeDrag {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div()
    }
}

fn metadata(text: impl Into<SharedString>) -> gpui::Div {
    div()
        .min_w_0()
        .font_family("JetBrains Mono")
        .text_size(px(10.))
        .line_height(px(13.))
        .text_color(rgb(MUTED))
        .overflow_hidden()
        .whitespace_nowrap()
        .text_ellipsis()
        .child(text.into())
}

fn sidebar_button(
    id: &'static str,
    path: &'static str,
    title: &'static str,
    on_click: impl Fn(&gpui::ClickEvent, &mut Window, &mut gpui::App) + 'static,
) -> impl IntoElement {
    div()
        .id(id)
        .w(px(18.))
        .h(px(30.))
        .flex_shrink_0()
        .flex()
        .items_center()
        .justify_center()
        .rounded(px(5.))
        .hover(|s| s.bg(hsla(0., 0., 1., 0.07)))
        .cursor_pointer()
        .on_click(on_click)
        .child(icon(path, 14., SECONDARY))
        .tooltip(move |_, cx| cx.new(|_| SidebarTooltip(title.into())).into())
}

struct SidebarTooltip(String);
impl Render for SidebarTooltip {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div()
            .px(px(9.))
            .py(px(6.))
            .max_w(px(360.))
            .rounded(px(6.))
            .bg(rgb(0x303633))
            .border_1()
            .border_color(rgb(BORDER))
            .child(label(self.0.clone(), 11., INK, FontWeight::NORMAL))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> state::ViewModel {
        let mut view = state::ViewModel::default();
        view.workspaces = vec![
            state::Workspace {
                id: "a".into(),
                name: "a".into(),
                path: "/home/adetola/".into(),
                ..Default::default()
            },
            state::Workspace {
                id: "b".into(),
                path: "/repos/talos".into(),
                branch: Some("feature/sidebar".into()),
                ..Default::default()
            },
        ];
        view.sessions = vec![
            state::Session {
                id: "1".into(),
                title: "Zulu".into(),
                workspace: "a".into(),
                updated_at: Some(100),
                time: "1m".into(),
                ..Default::default()
            },
            state::Session {
                id: "2".into(),
                title: "Alpha".into(),
                workspace: "b".into(),
                updated_at: Some(200),
                time: "1m".into(),
                ..Default::default()
            },
            state::Session {
                id: "3".into(),
                archived: true,
                ..Default::default()
            },
            state::Session {
                id: "4".into(),
                title: "Fallback".into(),
                cwd: "/tmp/other".into(),
                ..Default::default()
            },
        ];
        view
    }

    fn ids(view: &state::ViewModel, query: &str, sort: SessionSort) -> Vec<String> {
        session_rows(view, query, sort)
            .iter()
            .map(|r| r.session.id.clone())
            .collect()
    }

    fn multi_session_fixture() -> state::ViewModel {
        let mut view = fixture();
        view.sessions.push(state::Session {
            id: "5".into(),
            title: "Beta".into(),
            workspace: "a".into(),
            updated_at: Some(300),
            time: "1m".into(),
            ..Default::default()
        });
        view.sessions.push(state::Session {
            id: "6".into(),
            title: "Unknown date".into(),
            workspace: "a".into(),
            ..Default::default()
        });
        view.sessions[2].workspace = "a".into();
        view
    }

    #[test]
    fn rows_are_grouped_alphabetically_by_workspace_with_optional_branches() {
        let view = fixture();
        let rows = session_rows(&view, "", SessionSort::Recent);
        assert_eq!(rows.len(), 3);
        assert_eq!(rows[0].workspace, "adetola");
        assert_eq!(rows[0].branch, None);
        assert_eq!(rows[1].workspace, "other");
        assert_eq!(rows[2].workspace, "talos");
        assert_eq!(rows[2].branch, Some("feature/sidebar"));
    }

    #[test]
    fn sidebar_resize_stays_within_minimum_and_available_width() {
        assert_eq!(sidebar_width_at(0., 1200.), SIDEBAR_MIN_WIDTH);
        assert_eq!(sidebar_width_at(1200., 1200.), SIDEBAR_MAX_WIDTH);
        assert_eq!(sidebar_width_at(760., 760.), 392.);
    }

    #[test]
    fn search_matches_title_folder_full_path_and_branch_across_workspaces() {
        let view = fixture();
        assert_eq!(ids(&view, "ALPHA sidebar", SessionSort::Recent), ["2"]);
        assert_eq!(ids(&view, "/home/adetola", SessionSort::Recent), ["1"]);
        assert_eq!(ids(&view, "OTHER", SessionSort::Recent), ["4"]);
        assert_eq!(
            ids(&view, "missing", SessionSort::Recent),
            Vec::<String>::new()
        );
        assert_eq!(ids(&view, "  ", SessionSort::Recent).len(), 3);
    }

    #[test]
    fn every_sort_keeps_all_workspaces_together_and_sorts_sessions_within_them() {
        let view = multi_session_fixture();
        // Dates have the same rounded display label, but real timestamps order the rows.
        // Unknown dates stay last within their workspace, not at the end of the sidebar.
        for (sort, expected) in [
            (SessionSort::Recent, ["5", "1", "6", "4", "2"]),
            (SessionSort::Oldest, ["1", "5", "6", "4", "2"]),
            (SessionSort::Title, ["5", "6", "1", "4", "2"]),
        ] {
            assert_eq!(ids(&view, "", sort), expected, "{sort:?}");
            let rows = session_rows(&view, "", sort);
            let headings: Vec<_> = rows
                .chunk_by(|a, b| a.workspace_key == b.workspace_key)
                .map(|group| group[0].workspace.as_str())
                .collect();
            assert_eq!(headings, ["adetola", "other", "talos"]);
        }
    }

    #[test]
    fn search_keeps_matching_groups_without_empty_headings() {
        let mut view = multi_session_fixture();
        view.sessions[0].title = "Shared older".into();
        view.sessions[1].title = "Shared other workspace".into();
        view.sessions[4].title = "Shared newer".into();
        assert_eq!(ids(&view, "Shared", SessionSort::Recent), ["5", "1", "2"]);
        assert!(session_rows(&view, "missing", SessionSort::Recent).is_empty());
    }

    #[test]
    fn workspaces_with_the_same_folder_name_remain_distinct() {
        let mut view = multi_session_fixture();
        view.workspaces[1].path = "/repos/adetola".into();
        let rows = session_rows(&view, "", SessionSort::Recent);
        let groups: Vec<_> = rows
            .chunk_by(|a, b| a.workspace_key == b.workspace_key)
            .collect();
        assert_eq!(groups.len(), 3);
        assert_eq!(groups[0][0].workspace, "/home/adetola");
        assert_eq!(groups[1][0].workspace, "other");
        assert_eq!(groups[2][0].workspace, "repos/adetola");
        assert_eq!(groups[0].len(), 3);
        assert_eq!(groups[2].len(), 1);
        assert_eq!(groups[0][0].workspace_path, "/home/adetola/");
        assert_eq!(groups[2][0].workspace_path, "/repos/adetola");
    }

    #[test]
    fn unregistered_sessions_group_by_full_directory_not_just_basename() {
        let mut view = fixture();
        view.sessions.push(state::Session {
            id: "5".into(),
            cwd: "/tmp/other/".into(),
            updated_at: Some(300),
            ..Default::default()
        });
        view.sessions.push(state::Session {
            id: "6".into(),
            cwd: "/else/other".into(),
            ..Default::default()
        });
        let rows = session_rows(&view, "", SessionSort::Recent);
        let groups: Vec<_> = rows
            .chunk_by(|a, b| a.workspace_key == b.workspace_key)
            .collect();
        assert_eq!(groups.len(), 4);
        assert_eq!(groups[1][0].workspace_path, "/else/other");
        assert_eq!(groups[1].len(), 1);
        assert_eq!(groups[2][0].workspace_path, "/tmp/other/");
        assert_eq!(groups[2].len(), 2);
        assert_eq!(
            ids(&view, "", SessionSort::Recent),
            ["1", "6", "5", "4", "2"]
        );
    }

    #[test]
    fn ties_are_stable_across_inventory_order_changes() {
        let mut view = multi_session_fixture();
        view.sessions[4].updated_at = view.sessions[0].updated_at;
        view.sessions[4].title = view.sessions[0].title.to_uppercase();
        for sort in [SessionSort::Recent, SessionSort::Oldest, SessionSort::Title] {
            let before = ids(&view, "", sort);
            view.sessions.reverse();
            assert_eq!(before, ids(&view, "", sort));
        }
    }

    #[test]
    fn blank_branches_and_missing_paths_have_safe_fallbacks() {
        let mut view = fixture();
        view.workspaces[1].branch = Some("  ".into());
        view.sessions[3].cwd.clear();
        let rows = session_rows(&view, "", SessionSort::Recent);
        assert!(
            rows.iter()
                .find(|r| r.session.id == "2")
                .unwrap()
                .branch
                .is_none()
        );
        assert_eq!(
            rows.iter().find(|r| r.session.id == "4").unwrap().workspace,
            "No workspace"
        );
    }
}
