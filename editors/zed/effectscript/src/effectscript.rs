use zed_extension_api::{self as zed, settings::LspSettings, Command, LanguageServerId, Result};

struct EffectScriptExtension;

impl EffectScriptExtension {
    /// Locate the tsgo binary, in priority order:
    /// 1. `binary.path` in the LSP settings for this server.
    /// 2. A `tsgo` (or `tsgo.exe`) on the worktree PATH.
    /// 3. `./built/local/tsgo` relative to the worktree root (the default
    ///    `npx hereby build` output of typescript-go-effect).
    fn server_command(
        &self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Command> {
        let lsp_settings = LspSettings::for_worktree(language_server_id.as_ref(), worktree).ok();

        // Default LSP invocation; overridable via `binary.arguments`.
        let mut args = vec!["--lsp".to_string(), "--stdio".to_string()];
        let env: Vec<(String, String)> = worktree.shell_env();

        if let Some(binary) = lsp_settings.as_ref().and_then(|s| s.binary.as_ref()) {
            if let Some(extra) = &binary.arguments {
                args = extra.clone();
            }
            if let Some(path) = &binary.path {
                return Ok(Command {
                    command: path.clone(),
                    args,
                    env,
                });
            }
        }

        if let Some(path) = worktree.which("tsgo") {
            return Ok(Command {
                command: path,
                args,
                env,
            });
        }

        // Fall back to the default build output. Zed surfaces a spawn error if
        // it isn't there, with the build instructions in editors/zed/README.md.
        Ok(Command {
            command: format!("{}/built/local/tsgo", worktree.root_path()),
            args,
            env,
        })
    }
}

impl zed::Extension for EffectScriptExtension {
    fn new() -> Self {
        Self
    }

    fn language_server_command(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Command> {
        self.server_command(language_server_id, worktree)
    }

    fn language_server_initialization_options(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Option<zed::serde_json::Value>> {
        Ok(LspSettings::for_worktree(language_server_id.as_ref(), worktree)
            .ok()
            .and_then(|s| s.initialization_options))
    }

    fn language_server_workspace_configuration(
        &mut self,
        language_server_id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Option<zed::serde_json::Value>> {
        Ok(LspSettings::for_worktree(language_server_id.as_ref(), worktree)
            .ok()
            .and_then(|s| s.settings))
    }
}

zed::register_extension!(EffectScriptExtension);
