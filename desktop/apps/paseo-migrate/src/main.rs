use clap::Parser;
use std::path::PathBuf;

#[derive(Parser)]
#[command(about = "Safely inventory or import portable legacy Paseo data")]
struct Args {
    /// Legacy Paseo home (defaults to ~/.paseo).
    #[arg(long, default_value_os_t = default_source())]
    source: PathBuf,
    /// Separate Rust-owned home (defaults to `PASEO_RS_HOME` or ~/.paseo-rs).
    #[arg(long, default_value_os_t = default_destination())]
    destination: PathBuf,
    /// Perform the import. Without this flag the command is strictly dry-run.
    #[arg(long)]
    apply: bool,
}
fn home_dir() -> PathBuf {
    directories::UserDirs::new()
        .map_or_else(|| PathBuf::from("."), |dirs| dirs.home_dir().to_path_buf())
}
fn default_source() -> PathBuf {
    home_dir().join(".paseo")
}
fn default_destination() -> PathBuf {
    std::env::var_os("PASEO_RS_HOME").map_or_else(|| home_dir().join(".paseo-rs"), PathBuf::from)
}
fn main() {
    let args = Args::parse();
    match paseo_migrate::migrate(&args.source, &args.destination, args.apply) {
        Ok(report) => println!(
            "{}",
            serde_json::to_string_pretty(&report).expect("report serializes")
        ),
        Err(error) => {
            eprintln!("migration failed: {error}");
            std::process::exit(1);
        }
    }
}
