use std::time::{Duration, Instant};

use eframe::egui::{self, Align, Color32, Layout, Margin, RichText, Stroke};
use serde_json::Value;
use talos_ipc::Command;

use super::{Action, Desktop, NEW_CONVERSATION, SettingsTab, settings_heading};
use crate::{state::SessionStatus, theme, ui::timeline};

#[derive(Default)]
pub(super) struct Management {
    projects_requested: bool,
    pub(super) project_name: String,
    pub(super) project_root: String,
    project_id: Option<String>,
    workspace_id: Option<String>,
    workspace_rename_id: Option<String>,
    workspace_name: String,
    worktree_name: String,
    worktree_branch: String,
    worktree_base: String,
    history_query: String,
    history_current_only: bool,
    terminal_workspace: Option<String>,
    terminal_id: Option<String>,
    terminal_program: String,
    terminal_input: String,
    terminal_last_poll: Option<Instant>,
}

impl Desktop {
    pub(super) fn workspace_settings(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Projects and workspaces");
        ui.label(
            RichText::new("Projects group related folders. A managed worktree creates and removes a Git worktree through the daemon.")
                .font(theme::ui(11.5))
                .color(theme::MUTED),
        );
        ui.add_space(12.0);
        if !self.manage.projects_requested && self.view().online {
            self.manage.projects_requested =
                self.backend.submit(Action::Rpc(Command::ListProjects));
        }
        ui.horizontal(|ui| {
            if ui
                .add_enabled(
                    self.view().online,
                    egui::Button::new("Refresh projects and workspaces"),
                )
                .clicked()
            {
                self.manage.projects_requested =
                    self.backend.submit(Action::Rpc(Command::ListProjects));
                self.backend.submit(Action::Refresh);
            }
            if ui.button("Add workspace folder…").clicked() {
                self.new_workspace.open = true;
            }
        });
        ui.add_space(14.0);

        let projects = self.view().projects.clone();
        let workspaces = self.view().workspaces.clone();
        let project_ids: Vec<String> = projects
            .iter()
            .filter_map(|project| project["id"].as_str().map(str::to_owned))
            .collect();
        if self
            .manage
            .project_id
            .as_ref()
            .is_none_or(|id| !project_ids.contains(id))
        {
            self.manage.project_id = project_ids.first().cloned();
        }

        settings_heading(ui, "Projects");
        if projects.is_empty() {
            ui.label(
                RichText::new(if self.view().online {
                    "No projects yet. Add one using its existing Git root."
                } else {
                    "Projects are unavailable while the daemon is offline."
                })
                .font(theme::ui(11.5))
                .color(theme::MUTED),
            );
        }
        egui::ComboBox::from_id_salt("managed-project-picker")
            .selected_text(
                self.manage
                    .project_id
                    .as_ref()
                    .and_then(|id| {
                        projects
                            .iter()
                            .find(|project| project["id"].as_str() == Some(id.as_str()))
                    })
                    .and_then(|project| project["name"].as_str())
                    .unwrap_or("Select a project"),
            )
            .width(ui.available_width())
            .show_ui(ui, |ui| {
                for project in &projects {
                    let (Some(id), Some(name)) = (project["id"].as_str(), project["name"].as_str())
                    else {
                        continue;
                    };
                    if ui
                        .selectable_label(self.manage.project_id.as_deref() == Some(id), name)
                        .clicked()
                    {
                        self.manage.project_id = Some(id.to_owned());
                    }
                }
            });
        if let Some(project) = self.manage.project_id.as_ref().and_then(|id| {
            projects
                .iter()
                .find(|project| project["id"].as_str() == Some(id.as_str()))
        }) {
            ui.label(
                RichText::new(project["root_path"].as_str().unwrap_or_default())
                    .font(theme::mono(10.5))
                    .color(theme::FAINT),
            );
            ui.horizontal(|ui| {
                ui.label("Rename");
                let name = project["name"].as_str().unwrap_or_default();
                if self.manage.project_name.is_empty() {
                    self.manage.project_name = name.to_owned();
                }
                ui.add(
                    egui::TextEdit::singleline(&mut self.manage.project_name).desired_width(180.0),
                );
                let id = project["id"].as_str().unwrap_or_default().to_owned();
                if ui
                    .add_enabled(
                        self.view().online
                            && self.backend.busy == 0
                            && !self.manage.project_name.trim().is_empty()
                            && self.manage.project_name.trim() != name,
                        egui::Button::new("Save"),
                    )
                    .clicked()
                {
                    self.backend.submit(Action::Rpc(Command::RenameProject {
                        project_id: id.clone(),
                        name: self.manage.project_name.trim().to_owned(),
                    }));
                }
                if ui
                    .add_enabled(self.view().online, egui::Button::new("Delete project"))
                    .clicked()
                {
                    self.confirmation = Some((
                        "Delete this project? It must have no associated workspaces.".into(),
                        Action::Rpc(Command::DeleteProject { project_id: id }),
                    ));
                }
            });
        }
        ui.add_space(8.0);
        egui::CollapsingHeader::new("Create project")
            .default_open(projects.is_empty())
            .show(ui, |ui| {
                ui.label("Project name");
                ui.add(
                    egui::TextEdit::singleline(&mut self.manage.project_name)
                        .hint_text("talos")
                        .desired_width(f32::INFINITY),
                );
                ui.label("Absolute Git root path");
                ui.add(
                    egui::TextEdit::singleline(&mut self.manage.project_root)
                        .hint_text("/work/talos")
                        .desired_width(f32::INFINITY),
                );
                let valid = self.view().online
                    && self.backend.busy == 0
                    && !self.manage.project_name.trim().is_empty()
                    && self.manage.project_root.starts_with('/');
                if ui
                    .add_enabled(valid, egui::Button::new("Create project"))
                    .clicked()
                {
                    self.backend.submit(Action::Rpc(Command::CreateProject {
                        name: self.manage.project_name.trim().to_owned(),
                        root_path: self.manage.project_root.trim().to_owned(),
                    }));
                }
            });

        ui.add_space(16.0);
        settings_heading(ui, "Associate a workspace with a project");
        let active_workspaces: Vec<_> = workspaces
            .iter()
            .filter(|workspace| !workspace.archived)
            .collect();
        if self
            .manage
            .workspace_id
            .as_ref()
            .is_none_or(|id| !workspaces.iter().any(|workspace| &workspace.id == id))
        {
            self.manage.workspace_id = self
                .workspace_filter
                .clone()
                .filter(|id| {
                    active_workspaces
                        .iter()
                        .any(|workspace| &workspace.id == id)
                })
                .or_else(|| {
                    active_workspaces
                        .first()
                        .map(|workspace| workspace.id.clone())
                });
        }
        egui::ComboBox::from_id_salt("project-workspace-picker")
            .selected_text(
                self.manage
                    .workspace_id
                    .as_ref()
                    .and_then(|id| workspaces.iter().find(|workspace| &workspace.id == id))
                    .map_or("Select a workspace", |workspace| workspace.name.as_str()),
            )
            .width(ui.available_width())
            .show_ui(ui, |ui| {
                for workspace in &active_workspaces {
                    if ui
                        .selectable_label(
                            self.manage.workspace_id.as_deref() == Some(&workspace.id),
                            &workspace.name,
                        )
                        .clicked()
                    {
                        self.manage.workspace_id = Some(workspace.id.clone());
                    }
                }
            });
        let current_project = self
            .manage
            .workspace_id
            .as_ref()
            .and_then(|id| workspaces.iter().find(|workspace| &workspace.id == id))
            .and_then(|workspace| workspace.project.clone());
        let mut chosen_project = current_project.clone();
        egui::ComboBox::from_id_salt("workspace-project-association")
            .selected_text(
                chosen_project
                    .as_ref()
                    .and_then(|id| {
                        projects
                            .iter()
                            .find(|project| project["id"].as_str() == Some(id.as_str()))
                    })
                    .and_then(|project| project["name"].as_str())
                    .unwrap_or("No project"),
            )
            .width(ui.available_width())
            .show_ui(ui, |ui| {
                if ui
                    .selectable_label(chosen_project.is_none(), "No project")
                    .clicked()
                {
                    chosen_project = None;
                }
                for project in &projects {
                    let (Some(id), Some(name)) = (project["id"].as_str(), project["name"].as_str())
                    else {
                        continue;
                    };
                    if ui
                        .selectable_label(chosen_project.as_deref() == Some(id), name)
                        .clicked()
                    {
                        chosen_project = Some(id.to_owned());
                    }
                }
            });
        if chosen_project != current_project
            && let Some(workspace_id) = self.manage.workspace_id.clone()
        {
            self.backend
                .submit(Action::Rpc(Command::SetWorkspaceProject {
                    workspace_id,
                    project_id: chosen_project,
                }));
        }

        ui.add_space(16.0);
        settings_heading(ui, "Create a managed worktree");
        ui.label(
            RichText::new("The daemon creates a branch and worktree under the project’s managed worktree directory.")
                .font(theme::ui(11.0))
                .color(theme::MUTED),
        );
        ui.add_space(6.0);
        ui.horizontal(|ui| {
            ui.label("Name");
            ui.add(
                egui::TextEdit::singleline(&mut self.manage.worktree_name)
                    .hint_text("bugfix")
                    .desired_width(140.0),
            );
        });
        ui.horizontal(|ui| {
            ui.label("Branch");
            ui.add(
                egui::TextEdit::singleline(&mut self.manage.worktree_branch)
                    .hint_text("agent/bugfix")
                    .desired_width(140.0),
            );
            ui.label("Base");
            ui.add(
                egui::TextEdit::singleline(&mut self.manage.worktree_base)
                    .hint_text("main")
                    .desired_width(100.0),
            );
        });
        if ui
            .add_enabled(
                self.view().online
                    && self.backend.busy == 0
                    && self.manage.project_id.is_some()
                    && !self.manage.worktree_name.trim().is_empty()
                    && !self.manage.worktree_branch.trim().is_empty(),
                egui::Button::new("Create worktree"),
            )
            .clicked()
            && let Some(project_id) = self.manage.project_id.clone()
        {
            self.backend
                .submit(Action::Rpc(Command::CreateManagedWorktree {
                    project_id,
                    name: self.manage.worktree_name.trim().to_owned(),
                    branch: self.manage.worktree_branch.trim().to_owned(),
                    base_ref: if self.manage.worktree_base.trim().is_empty() {
                        "HEAD".into()
                    } else {
                        self.manage.worktree_base.trim().to_owned()
                    },
                }));
        }

        ui.add_space(18.0);
        settings_heading(ui, "Workspace folders");
        for workspace in &workspaces {
            egui::Frame::new()
                .fill(theme::SURFACE)
                .stroke(Stroke::new(1.0, theme::LINE))
                .corner_radius(9)
                .inner_margin(Margin::symmetric(10, 8))
                .show(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.vertical(|ui| {
                            ui.label(
                                RichText::new(&workspace.name)
                                    .font(theme::medium(12.0))
                                    .color(theme::TEXT),
                            );
                            ui.label(
                                RichText::new(&workspace.path)
                                    .font(theme::mono(9.5))
                                    .color(theme::FAINT),
                            );
                        });
                        if workspace.managed {
                            ui.label(RichText::new("managed worktree").font(theme::ui(9.5)).color(theme::BLUE));
                        }
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if workspace.archived {
                                if ui
                                    .add_enabled(self.view().online, egui::Button::new("Restore"))
                                    .clicked()
                                {
                                    self.backend.submit(Action::Rpc(Command::RestoreWorkspace {
                                        workspace_id: workspace.id.clone(),
                                    }));
                                }
                            } else {
                                if workspace.managed
                                    && ui
                                        .add_enabled(self.view().online, egui::Button::new("Remove worktree"))
                                        .clicked()
                                {
                                    self.confirmation = Some((
                                        format!("Remove managed worktree ‘{}’? Active terminals must be closed first.", workspace.name),
                                        Action::Rpc(Command::DeleteManagedWorktree {
                                            workspace_id: workspace.id.clone(),
                                        }),
                                    ));
                                }
                                if ui
                                    .add_enabled(self.view().online, egui::Button::new("Archive"))
                                    .clicked()
                                {
                                    self.confirmation = Some((
                                        format!("Archive workspace ‘{}’?", workspace.name),
                                        Action::Rpc(Command::ArchiveWorkspace {
                                            workspace_id: workspace.id.clone(),
                                        }),
                                    ));
                                }
                                if ui.small_button("Use").clicked() {
                                    self.workspace_filter = Some(workspace.id.clone());
                                }
                            }
                        });
                    });
                    ui.horizontal(|ui| {
                        if self.manage.workspace_rename_id.as_deref() == Some(&workspace.id) {
                            ui.add(
                                egui::TextEdit::singleline(&mut self.manage.workspace_name)
                                    .desired_width(180.0),
                            );
                            if ui
                                .add_enabled(
                                    self.view().online
                                        && !self.manage.workspace_name.trim().is_empty()
                                        && self.manage.workspace_name.trim() != workspace.name,
                                    egui::Button::new("Save name"),
                                )
                                .clicked()
                            {
                                self.backend.submit(Action::Rpc(Command::RenameWorkspace {
                                    workspace_id: workspace.id.clone(),
                                    name: self.manage.workspace_name.trim().to_owned(),
                                }));
                            }
                            if ui.small_button("Cancel").clicked() {
                                self.manage.workspace_rename_id = None;
                                self.manage.workspace_name.clear();
                            }
                        } else if ui.small_button("Rename").clicked() {
                            self.manage.workspace_rename_id = Some(workspace.id.clone());
                            self.manage.workspace_name = workspace.name.clone();
                        }
                    });
                });
            ui.add_space(6.0);
        }
    }

    pub(super) fn history_settings(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Search conversation history");
        ui.label(
            RichText::new("Searches the durable timeline across workspaces. Select a result to open its conversation.")
                .font(theme::ui(11.5))
                .color(theme::MUTED),
        );
        ui.add_space(8.0);
        ui.horizontal(|ui| {
            let search = ui.add(
                egui::TextEdit::singleline(&mut self.manage.history_query)
                    .hint_text("Search messages, tool activity and notices")
                    .desired_width(ui.available_width() - 90.0),
            );
            let enter =
                search.lost_focus() && ui.input(|input| input.key_pressed(egui::Key::Enter));
            let run = ui
                .add_enabled(
                    self.view().online && !self.manage.history_query.trim().is_empty(),
                    egui::Button::new("Search"),
                )
                .clicked()
                || enter;
            if run && self.view().online && !self.manage.history_query.trim().is_empty() {
                let session_id = self
                    .manage
                    .history_current_only
                    .then(|| self.session_id())
                    .flatten();
                self.backend.submit(Action::Rpc(Command::SearchTimeline {
                    query: self.manage.history_query.trim().to_owned(),
                    session_id,
                    limit: Some(100),
                }));
            }
        });
        ui.checkbox(
            &mut self.manage.history_current_only,
            "Only search the current conversation",
        );
        let query = self.manage.history_query.trim();
        if query.is_empty() || self.view().search["query"].as_str() != Some(query) {
            return;
        }
        let events = self.view().search["events"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        ui.add_space(12.0);
        ui.label(
            RichText::new(format!(
                "{} result{}",
                events.len(),
                if events.len() == 1 { "" } else { "s" }
            ))
            .font(theme::semibold(11.0))
            .color(theme::SECONDARY),
        );
        for event in events {
            let session_id = event["session_id"].as_str().unwrap_or_default();
            let session = self.view().session(session_id);
            let title = session.map_or("Conversation", |session| session.title.as_str());
            let text = search_text(&event["kind"]);
            let timestamp = event["timestamp"].as_str().unwrap_or_default();
            let label = format!("{title}  ·  {}", truncate_text(&text, 92));
            if ui
                .add_sized(
                    [ui.available_width(), 46.0],
                    egui::Button::new(
                        RichText::new(label)
                            .font(theme::ui(11.0))
                            .color(theme::SECONDARY),
                    )
                    .fill(theme::SURFACE)
                    .stroke(Stroke::new(1.0, theme::LINE))
                    .corner_radius(8),
                )
                .on_hover_text(format!("{timestamp}\n{text}"))
                .clicked()
                && !session_id.is_empty()
            {
                self.select(session_id);
            }
            ui.add_space(4.0);
        }
        if self.view().search["has_more"] == true {
            ui.label(
                RichText::new("More matches are available; narrow the query to see them.")
                    .font(theme::ui(10.5))
                    .color(theme::FAINT),
            );
        }
    }

    pub(super) fn terminal_settings(&mut self, ui: &mut egui::Ui) {
        settings_heading(ui, "Workspace terminal");
        ui.label(
            RichText::new("A daemon-managed PTY scoped to one workspace. Commands run with the selected workspace directory as the default working directory.")
                .font(theme::ui(11.5))
                .color(theme::MUTED),
        );
        ui.add_space(10.0);
        let workspaces = self.view().workspaces.clone();
        let active: Vec<_> = workspaces
            .iter()
            .filter(|workspace| !workspace.archived)
            .collect();
        if self
            .manage
            .terminal_workspace
            .as_ref()
            .is_none_or(|id| !active.iter().any(|w| &w.id == id))
        {
            self.manage.terminal_workspace = self
                .session()
                .map(|session| session.workspace.clone())
                .filter(|id| active.iter().any(|workspace| &workspace.id == id))
                .or_else(|| active.first().map(|workspace| workspace.id.clone()));
        }
        let current_name = self
            .manage
            .terminal_workspace
            .as_ref()
            .and_then(|id| workspaces.iter().find(|workspace| &workspace.id == id))
            .map_or("Select a workspace", |workspace| workspace.name.as_str());
        let previous_workspace = self.manage.terminal_workspace.clone();
        egui::ComboBox::from_id_salt("terminal-workspace-picker")
            .selected_text(current_name)
            .width(ui.available_width())
            .show_ui(ui, |ui| {
                for workspace in &active {
                    if ui
                        .selectable_label(
                            self.manage.terminal_workspace.as_deref() == Some(&workspace.id),
                            &workspace.name,
                        )
                        .clicked()
                    {
                        self.manage.terminal_workspace = Some(workspace.id.clone());
                    }
                }
            });
        if previous_workspace != self.manage.terminal_workspace {
            self.manage.terminal_id = None;
            self.manage.terminal_last_poll = None;
            if let Some(workspace_id) = self.manage.terminal_workspace.clone() {
                self.backend
                    .submit(Action::Rpc(Command::ListTerminals { workspace_id }));
            }
        }
        let Some(workspace_id) = self.manage.terminal_workspace.clone() else {
            ui.label(
                RichText::new("Add a workspace before starting a terminal.").color(theme::FAINT),
            );
            return;
        };
        let terminal_list: Vec<Value> = self
            .view()
            .terminals
            .as_array()
            .into_iter()
            .flatten()
            .filter(|terminal| terminal["workspace_id"].as_str() == Some(workspace_id.as_str()))
            .cloned()
            .collect();
        if self.manage.terminal_id.as_ref().is_none_or(|id| {
            !terminal_list
                .iter()
                .any(|terminal| terminal["id"].as_str() == Some(id.as_str()))
        }) {
            self.manage.terminal_id = terminal_list
                .first()
                .and_then(|terminal| terminal["id"].as_str())
                .map(str::to_owned);
        }
        ui.horizontal(|ui| {
            if ui
                .add_enabled(self.view().online, egui::Button::new("Refresh terminals"))
                .clicked()
            {
                self.backend.submit(Action::Rpc(Command::ListTerminals {
                    workspace_id: workspace_id.clone(),
                }));
            }
            if ui
                .add_enabled(
                    self.view().online && self.backend.busy == 0,
                    egui::Button::new("New terminal"),
                )
                .clicked()
            {
                let program = if self.manage.terminal_program.trim().is_empty() {
                    "sh"
                } else {
                    self.manage.terminal_program.trim()
                };
                self.backend.submit(Action::Rpc(Command::CreateTerminal {
                    workspace_id: workspace_id.clone(),
                    cwd: None,
                    program: program.to_owned(),
                    args: Vec::new(),
                    rows: 30,
                    cols: 100,
                }));
                self.manage.terminal_last_poll = None;
            }
        });
        ui.horizontal(|ui| {
            ui.label("Program");
            ui.add(
                egui::TextEdit::singleline(&mut self.manage.terminal_program)
                    .hint_text("sh")
                    .desired_width(180.0),
            );
            ui.label(
                RichText::new("starts in interactive mode")
                    .font(theme::ui(9.5))
                    .color(theme::FAINT),
            );
        });
        if terminal_list.is_empty() {
            ui.label(
                RichText::new("No terminal is running in this workspace.")
                    .font(theme::ui(11.5))
                    .color(theme::FAINT),
            );
            return;
        }
        egui::ComboBox::from_id_salt("terminal-picker")
            .selected_text(
                self.manage
                    .terminal_id
                    .as_deref()
                    .unwrap_or("Select a terminal"),
            )
            .width(ui.available_width())
            .show_ui(ui, |ui| {
                for terminal in &terminal_list {
                    let Some(id) = terminal["id"].as_str() else {
                        continue;
                    };
                    let label = format!("{} · {}", id, terminal["cwd"].as_str().unwrap_or(""));
                    if ui
                        .selectable_label(self.manage.terminal_id.as_deref() == Some(id), label)
                        .clicked()
                    {
                        self.manage.terminal_id = Some(id.to_owned());
                        self.manage.terminal_last_poll = None;
                    }
                }
            });
        let Some(terminal_id) = self.manage.terminal_id.clone() else {
            return;
        };
        let running = terminal_list
            .iter()
            .find(|terminal| terminal["id"] == terminal_id)
            .is_some_and(|terminal| terminal["running"] == true);
        ui.horizontal(|ui| {
            ui.label(if running { "Running" } else { "Exited" });
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                if ui
                    .add_enabled(self.view().online, egui::Button::new("Stop terminal"))
                    .clicked()
                {
                    self.confirmation = Some((
                        "Stop this terminal process?".into(),
                        Action::Rpc(Command::KillTerminal {
                            workspace_id: workspace_id.clone(),
                            terminal_id: terminal_id.clone(),
                        }),
                    ));
                }
                if ui
                    .add_enabled(self.view().online, egui::Button::new("Capture output"))
                    .clicked()
                {
                    self.backend.submit(Action::Rpc(Command::CaptureTerminal {
                        workspace_id: workspace_id.clone(),
                        terminal_id: terminal_id.clone(),
                        limit: Some(65_536),
                    }));
                }
            });
        });
        ui.add_space(4.0);
        let capture = self.view().terminal_capture.clone();
        if capture["terminal_id"].as_str() == Some(&terminal_id) {
            let mut output = capture["data"].as_str().unwrap_or_default().to_owned();
            egui::Frame::new()
                .fill(theme::CODE)
                .stroke(Stroke::new(1.0, theme::LINE))
                .corner_radius(8)
                .inner_margin(Margin::symmetric(8, 6))
                .show(ui, |ui| {
                    ui.add(
                        egui::TextEdit::multiline(&mut output)
                            .font(theme::mono(10.5))
                            .desired_rows(18)
                            .desired_width(f32::INFINITY)
                            .interactive(false),
                    );
                });
            if capture["truncated"] == true {
                ui.label(
                    RichText::new("Earlier output was truncated by the daemon buffer.")
                        .font(theme::ui(10.0))
                        .color(theme::FAINT),
                );
            }
        } else {
            ui.label(
                RichText::new("Capture terminal output to view it here.")
                    .font(theme::ui(10.5))
                    .color(theme::FAINT),
            );
        }
        ui.horizontal(|ui| {
            let input = ui.add(
                egui::TextEdit::singleline(&mut self.manage.terminal_input)
                    .hint_text("Type input and press Enter")
                    .desired_width(ui.available_width() - 68.0),
            );
            let enter =
                input.lost_focus() && ui.input(|events| events.key_pressed(egui::Key::Enter));
            let send = ui.add_enabled(
                self.view().online && running && !self.manage.terminal_input.is_empty(),
                egui::Button::new("Send"),
            );
            if (send.clicked() || enter) && self.view().online && running {
                let data = format!("{}\n", self.manage.terminal_input);
                if self.backend.submit(Action::Rpc(Command::TerminalInput {
                    workspace_id: workspace_id.clone(),
                    terminal_id: terminal_id.clone(),
                    data,
                })) {
                    self.manage.terminal_input.clear();
                }
            }
        });
        if running {
            let now = Instant::now();
            if self
                .manage
                .terminal_last_poll
                .is_none_or(|last| now.duration_since(last) >= Duration::from_millis(700))
                && self.backend.busy == 0
                && self.view().online
            {
                self.backend.submit(Action::Rpc(Command::CaptureTerminal {
                    workspace_id,
                    terminal_id,
                    limit: Some(65_536),
                }));
                self.manage.terminal_last_poll = Some(now);
            }
            ui.ctx().request_repaint_after(Duration::from_millis(400));
        }
    }

    pub(super) fn permission_panel(&mut self, root: &mut egui::Ui) {
        let pending_count = self.view().pending.len();
        if pending_count == 0 {
            return;
        }
        let selected = self.session_id();
        let entries = selected
            .as_ref()
            .and_then(|id| self.view().entries.get(id))
            .cloned()
            .unwrap_or_default();
        let selected_pending = entries
            .iter()
            .filter(|entry| matches!(&entry.kind, crate::state::EntryKind::Permission { id, .. } if self.view().pending.contains_key(id)))
            .count();
        let height = if selected_pending > 0 { 176.0 } else { 58.0 };
        egui::Panel::top("pending-permission-panel")
            .exact_size(height)
            .resizable(false)
            .frame(
                egui::Frame::new()
                    .fill(theme::CANVAS)
                    .inner_margin(Margin::symmetric(18, 4)),
            )
            .show(root, |ui| {
                if selected_pending > 0 {
                    let Some(session) = selected.as_deref() else {
                        return;
                    };
                    let pending = self.view().pending.clone();
                    let online = self.view().online;
                    let actions = egui::ScrollArea::vertical()
                        .max_height(height - 8.0)
                        .show(ui, |ui| {
                            timeline::pending_requests(ui, session, &entries, &pending, online)
                        })
                        .inner;
                    self.submit_decisions(actions);
                } else {
                    ui.horizontal(|ui| {
                        ui.label(
                            RichText::new(format!(
                                "{pending_count} permission request{} need review",
                                if pending_count == 1 { "" } else { "s" }
                            ))
                            .font(theme::medium(11.5))
                            .color(theme::AMBER),
                        );
                        let target = self
                            .view()
                            .sessions
                            .iter()
                            .find(|session| session.status == SessionStatus::Attention)
                            .map(|session| (session.id.clone(), session.title.clone()));
                        if let Some((id, title)) = target
                            && ui.button(format!("Review {title}")).clicked()
                        {
                            self.select(&id);
                        }
                    });
                }
            });
    }

    fn submit_decisions(&mut self, actions: Vec<timeline::TimelineAction>) {
        for action in actions {
            let timeline::TimelineAction::Decide { session, id, allow } = action;
            if self.backend.submit(Action::Decision {
                id: id.clone(),
                session,
                allow,
            }) {
                self.backend
                    .view
                    .pending
                    .insert(id, crate::state::PendingState::Submitting);
            }
        }
    }

    pub(super) fn recovery_panel(&mut self, root: &mut egui::Ui) {
        let selected = self.session_id();
        let load_error = selected
            .as_ref()
            .and_then(|id| self.backend.load_errors.get(id))
            .cloned();
        let retry = self.backend.retry_prompt.clone();
        let retry_for_selected = retry.as_ref().and_then(|action| match action {
            Action::Prompt { session, text, .. } if selected.as_deref() == Some(session) => {
                Some((session.clone(), text.clone(), action.clone()))
            }
            _ => None,
        });
        if load_error.is_none() && retry_for_selected.is_none() && self.view().online {
            return;
        }
        let online = self.view().online;
        egui::Panel::top("connection-recovery-panel")
            .exact_size(52.0)
            .resizable(false)
            .frame(
                egui::Frame::new()
                    .fill(theme::CANVAS)
                    .inner_margin(Margin::symmetric(18, 4)),
            )
            .show(root, |ui| {
                ui.horizontal(|ui| {
                    if !online {
                        ui.label(
                            RichText::new(
                                "Disconnected · drafts and permission decisions are retained.",
                            )
                            .font(theme::ui(10.5))
                            .color(theme::AMBER),
                        );
                        if ui.button("Reconnect").clicked() {
                            self.backend.submit(Action::Refresh);
                        }
                    }
                    if let Some(error) = load_error.as_ref() {
                        ui.label(
                            RichText::new(format!("History could not be loaded: {error}"))
                                .font(theme::ui(10.0))
                                .color(theme::RED),
                        );
                        if ui
                            .add_enabled(online, egui::Button::new("Retry load"))
                            .clicked()
                            && let Some(id) = selected.clone()
                        {
                            self.backend.submit(Action::Load(id));
                        }
                    }
                    if let Some((session, text, action)) = retry_for_selected {
                        ui.label(
                            RichText::new("Prompt was not accepted.")
                                .font(theme::ui(10.5))
                                .color(theme::AMBER),
                        );
                        if ui.add_enabled(online, egui::Button::new("Retry")).clicked()
                            && self.backend.submit(action)
                        {
                            self.backend.view.echo_user_prompt(&session, &text);
                            if self
                                .drafts
                                .get(&session)
                                .is_some_and(|draft| draft.trim() == text.trim())
                            {
                                self.drafts.insert(session, String::new());
                            }
                        }
                        if ui.button("Keep as draft").clicked() {
                            self.backend.retry_prompt = None;
                        }
                    }
                });
            });
    }

    pub(super) fn attachment_panel(&mut self, ctx: &egui::Context) {
        let Some((session, mut path)) = self.attachment_dialog.take() else {
            return;
        };
        let mut open = true;
        let mut paths_to_validate = None;
        let mut cancel = false;
        egui::Window::new("Attach images")
            .open(&mut open)
            .collapsible(false)
            .resizable(false)
            .default_width(430.0)
            .frame(
                egui::Frame::new()
                    .fill(theme::SURFACE_HIGH)
                    .stroke(Stroke::new(1.0, theme::LINE_STRONG))
                    .corner_radius(14)
                    .inner_margin(Margin::symmetric(16, 14)),
            )
            .show(ctx, |ui| {
                ui.label(
                    RichText::new("Choose an image by dropping it into the window or entering its absolute path.")
                        .font(theme::ui(11.5))
                        .color(theme::SECONDARY),
                );
                ui.add_space(8.0);
                ui.add(
                    egui::TextEdit::singleline(&mut path)
                        .hint_text("/path/to/image.png")
                        .desired_width(f32::INFINITY),
                );
                ui.add_space(8.0);
                ui.label(
                    RichText::new("PNG, JPEG, GIF or WebP · up to 16 MiB each, 32 MiB total")
                        .font(theme::ui(10.0))
                        .color(theme::FAINT),
                );
                ui.horizontal(|ui| {
                    if ui.button("Cancel").clicked() { cancel = true; }
                    if ui
                        .add_enabled(self.view().online && !path.trim().is_empty(), egui::Button::new("Validate and attach"))
                        .clicked()
                    {
                        let mut paths: Vec<String> = self.images.get(&session)
                            .into_iter()
                            .flatten()
                            .filter_map(|image| image["path"].as_str().map(str::to_owned))
                            .collect();
                        let candidate = path.trim().to_owned();
                        if !paths.contains(&candidate) {
                            paths.push(candidate);
                        }
                        paths_to_validate = Some(paths);
                    }
                });
            });
        if open && !cancel {
            self.attachment_dialog = Some((session.clone(), path));
        }
        if let Some(paths) = paths_to_validate
            && self
                .backend
                .submit(Action::ValidateImages { session, paths })
        {
            self.notify("Validating image attachments…");
        }
    }

    pub(super) fn handle_dropped_files(&mut self, ctx: &egui::Context) {
        let (hovered, dropped) = ctx.input(|input| {
            (
                !input.raw.hovered_files.is_empty(),
                input
                    .raw
                    .dropped_files
                    .iter()
                    .map(|file| file.path().to_string_lossy().into_owned())
                    .collect::<Vec<_>>(),
            )
        });
        if hovered {
            let screen = ctx.content_rect();
            let painter = ctx.layer_painter(egui::LayerId::new(
                egui::Order::Foreground,
                egui::Id::unique("image-drop-overlay"),
            ));
            painter.rect_filled(screen, 0.0, Color32::from_black_alpha(128));
            painter.text(
                screen.center(),
                egui::Align2::CENTER_CENTER,
                "Drop images to attach",
                theme::semibold(18.0),
                theme::TEXT,
            );
        }
        if dropped.is_empty() {
            return;
        }
        let provider_id = self
            .session()
            .map(|session| session.provider.as_str())
            .or_else(|| {
                self.pending_model
                    .as_ref()
                    .map(|(provider, _)| provider.as_str())
            })
            .unwrap_or("talos");
        if !self
            .view()
            .provider(provider_id)
            .is_some_and(|provider| provider.supports("images"))
        {
            self.warn("The selected provider does not accept image attachments.");
            return;
        }
        let session = self.session_id().unwrap_or_else(|| NEW_CONVERSATION.into());
        let mut paths: Vec<String> = self
            .images
            .get(&session)
            .into_iter()
            .flatten()
            .filter_map(|image| image["path"].as_str().map(str::to_owned))
            .collect();
        for path in dropped {
            if !paths.contains(&path) {
                paths.push(path);
            }
        }
        if self
            .backend
            .submit(Action::ValidateImages { session, paths })
        {
            self.notify("Validating dropped images…");
        }
    }

    pub(super) fn management_overlays(&mut self, ctx: &egui::Context) {
        self.session_actions_overlay(ctx);
        self.confirmation_overlay(ctx);
    }

    fn session_actions_overlay(&mut self, ctx: &egui::Context) {
        if !self.session_actions {
            return;
        }
        let Some(session) = self.session().cloned() else {
            self.session_actions = false;
            return;
        };
        let mut open = true;
        egui::Window::new("Conversation actions")
            .open(&mut open)
            .collapsible(false)
            .resizable(false)
            .default_width(300.0)
            .show(ctx, |ui| {
                ui.label(
                    RichText::new(&session.title)
                        .font(theme::semibold(13.0))
                        .color(theme::TEXT),
                );
                ui.add_space(8.0);
                if ui.button("Copy conversation").clicked() {
                    self.copy_conversation();
                    self.session_actions = false;
                }
                if ui
                    .add_enabled(
                        self.view().online,
                        egui::Button::new("Reload conversation history"),
                    )
                    .clicked()
                {
                    self.backend.submit(Action::Load(session.id.clone()));
                    self.session_actions = false;
                }
                if ui
                    .add_enabled(!session.archived, egui::Button::new("Rename agent"))
                    .clicked()
                {
                    self.rename = Some((
                        session.id.clone(),
                        session.title.clone(),
                        session.title.clone(),
                    ));
                    self.session_actions = false;
                }
                if ui
                    .add_enabled(
                        self.view().online && !session.archived,
                        egui::Button::new("Archive agent"),
                    )
                    .clicked()
                {
                    self.confirmation = Some((
                        format!(
                            "Archive ‘{}’? You can still inspect its conversation history.",
                            session.title
                        ),
                        Action::Archive(session.id.clone()),
                    ));
                    self.session_actions = false;
                }
                if ui.button("Search this conversation").clicked() {
                    self.manage.history_current_only = true;
                    self.settings = Some(SettingsTab::History);
                    self.session_actions = false;
                }
            });
        self.session_actions = open && self.session_actions;
    }

    fn confirmation_overlay(&mut self, ctx: &egui::Context) {
        let Some((message, action)) = self.confirmation.take() else {
            return;
        };
        let mut open = true;
        let mut confirm = false;
        let mut cancel = false;
        egui::Window::new("Confirm action")
            .open(&mut open)
            .collapsible(false)
            .resizable(false)
            .default_width(360.0)
            .show(ctx, |ui| {
                ui.label(
                    RichText::new(&message)
                        .font(theme::ui(12.5))
                        .color(theme::SECONDARY),
                );
                ui.add_space(12.0);
                ui.horizontal(|ui| {
                    if ui.button("Cancel").clicked() {
                        cancel = true;
                    }
                    if ui
                        .add_enabled(self.view().online, egui::Button::new("Confirm"))
                        .clicked()
                    {
                        confirm = true;
                    }
                });
            });
        if confirm {
            self.backend.submit(action);
        } else if open && !cancel {
            self.confirmation = Some((message, action));
        }
    }
}

fn search_text(kind: &Value) -> String {
    let Some(object) = kind.as_object() else {
        return kind.to_string();
    };
    let Some((name, payload)) = object.iter().next() else {
        return kind.to_string();
    };
    let content = payload
        .get("text")
        .or_else(|| payload.get("delta"))
        .or_else(|| payload.get("name"))
        .and_then(Value::as_str)
        .map(str::to_owned)
        .unwrap_or_else(|| payload.to_string());
    format!("{name}: {content}")
}

fn truncate_text(text: &str, limit: usize) -> String {
    if text.chars().count() <= limit {
        return text.to_owned();
    }
    let mut result: String = text.chars().take(limit.saturating_sub(1)).collect();
    result.push('…');
    result
}
