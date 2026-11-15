use std::{
    collections::HashMap,
    path::{Component, Path, PathBuf},
};

/// Returns the shortest path suffix that uniquely identifies each workspace.
/// Paths inside the user's home directory are shown relative to home unless a
/// collision requires more context; external paths use their own components.
pub(super) fn for_paths<'a>(paths: impl IntoIterator<Item = &'a str>) -> Vec<String> {
    let home = std::env::var_os("HOME").map(PathBuf::from);
    labels_with_home(paths, home.as_deref())
}

fn labels_with_home<'a>(
    paths: impl IntoIterator<Item = &'a str>,
    home: Option<&Path>,
) -> Vec<String> {
    let paths: Vec<_> = paths
        .into_iter()
        .map(|path| {
            let path = Path::new(path);
            let relative = home.and_then(|home| path.strip_prefix(home).ok());
            let mut components = relative.map_or_else(|| path_components(path), path_components);
            if components.is_empty() {
                let fallback_name = relative
                    .and_then(|_| home.and_then(Path::file_name))
                    .or_else(|| path.file_name());
                let fallback = if let Some(name) = fallback_name {
                    name.to_string_lossy().into_owned()
                } else {
                    path.to_string_lossy().into_owned()
                };
                components.push(fallback);
            }
            PathLabel {
                components,
                full: normalized_path(path),
            }
        })
        .collect();

    let mut depths = vec![1; paths.len()];
    let mut use_full_path = vec![false; paths.len()];

    loop {
        let mut by_label: HashMap<String, Vec<usize>> = HashMap::new();
        for (index, path) in paths.iter().enumerate() {
            let label = if use_full_path[index] {
                path.full.clone()
            } else {
                let start = path.components.len().saturating_sub(depths[index]);
                path.components[start..].join("/")
            };
            by_label.entry(label).or_default().push(index);
        }

        let collisions: Vec<_> = by_label
            .into_values()
            .filter(|indices| indices.len() > 1)
            .collect();
        if collisions.is_empty() {
            break;
        }

        let mut changed = false;
        for indices in collisions {
            for index in indices {
                if use_full_path[index] {
                    continue;
                }
                if depths[index] < paths[index].components.len() {
                    depths[index] += 1;
                } else {
                    use_full_path[index] = true;
                }
                changed = true;
            }
        }
        // Identical paths cannot be disambiguated by a path-derived label.
        if !changed {
            break;
        }
    }

    paths
        .iter()
        .enumerate()
        .map(|(index, path)| {
            if use_full_path[index] {
                path.full.clone()
            } else {
                let start = path.components.len().saturating_sub(depths[index]);
                path.components[start..].join("/")
            }
        })
        .collect()
}

struct PathLabel {
    components: Vec<String>,
    full: String,
}

fn normalized_path(path: &Path) -> String {
    let mut normalized = PathBuf::new();
    for component in path.components() {
        normalized.push(component.as_os_str());
    }
    normalized.to_string_lossy().into_owned()
}

fn path_components(path: &Path) -> Vec<String> {
    path.components()
        .filter_map(|component| match component {
            Component::Normal(value) => Some(value.to_string_lossy().into_owned()),
            Component::ParentDir => Some("..".to_owned()),
            Component::CurDir | Component::RootDir | Component::Prefix(_) => None,
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn labels(paths: &[&str]) -> Vec<String> {
        labels_with_home(paths.iter().copied(), Some(Path::new("/home/adetola")))
    }

    #[test]
    fn unique_paths_use_only_the_folder_name() {
        assert_eq!(
            labels(&["/home/adetola/repos/cltp", "/home/adetola/repos"]),
            ["cltp", "repos"]
        );
    }

    #[test]
    fn duplicate_folder_names_expand_both_labels_by_the_minimum_context() {
        assert_eq!(
            labels(&["/home/adetola/repos/cltp", "/home/adetola/archive/cltp",]),
            ["repos/cltp", "archive/cltp"]
        );
    }

    #[test]
    fn duplicate_suffixes_expand_until_the_paths_are_unique() {
        assert_eq!(
            labels(&["/home/adetola/one/src/cltp", "/home/adetola/two/src/cltp",]),
            ["one/src/cltp", "two/src/cltp"]
        );
    }

    #[test]
    fn external_paths_use_the_shortest_unique_suffix_too() {
        assert_eq!(
            labels(&["/mnt/drive-a/repos/cltp", "/mnt/drive-b/repos/cltp"]),
            ["drive-a/repos/cltp", "drive-b/repos/cltp"]
        );
    }

    #[test]
    fn home_path_collisions_fall_back_to_full_paths_when_needed() {
        assert_eq!(
            labels(&["/home/adetola", "/repos/adetola"]),
            ["/home/adetola", "repos/adetola"]
        );
    }
}
