# Use Claude Code and Codex through CLIProxyAPI over Tailscale

Configure each client machine to send model requests to the same CLIProxyAPI server. The server owns the upstream provider accounts; client machines need only a proxy client API key.

## Before you start

1. Keep CLIProxyAPI running on the server and connect its upstream Claude/Codex accounts through the management dashboard.
2. Connect the server and every client machine to your Tailscale network. Tailnet access rules must allow the clients to reach the server on port 443.
3. Expose the local proxy with Tailscale Serve. For the default listener:

   ```sh
   tailscale serve --bg --https=443 http://127.0.0.1:8317
   tailscale serve status
   ```

   Complete the enablement link if Tailscale asks you to enable Serve/HTTPS. Serve makes this endpoint available within your tailnet. Keep the server bound to `127.0.0.1`; Tailscale forwards requests to it.
4. Note the HTTPS URL printed by Serve, such as `https://proxy-host.your-tailnet.ts.net`. The dashboard is at `/management.html` on that URL.
5. Obtain a **client API key** from the proxy's access settings (`access.api-keys` in v8 config). Transfer it securely to each client. A separate client key per machine lets you revoke access individually.

Use the client API key for Claude Code and Codex. The **management key** signs into the dashboard and must not be used as a client credential. Upstream OAuth tokens stay on the proxy server.

The server must be awake, online, and running the proxy. On a different client machine, `localhost` points to that client, not the proxy server. Always use the server's Tailscale HTTPS URL in the examples below.

## Check access from each client

Set these variables in a terminal on the client, replacing both placeholders:

```sh
export CLIPROXY_URL="https://proxy-host.your-tailnet.ts.net"
export CLIPROXY_API_KEY="REPLACE_WITH_CLIENT_API_KEY"
curl --fail-with-body "$CLIPROXY_URL/v1/models" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY"
```

A successful JSON model list verifies network access and client authentication. It does not verify provider quota or inference. Do not paste a real key into shared shell history, screenshots, or repository files.

## Claude Code

Merge this `env` block into `~/.claude/settings.json` on **each client machine**, preserving other settings and existing environment entries:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://proxy-host.your-tailnet.ts.net",
    "ANTHROPIC_AUTH_TOKEN": "REPLACE_WITH_CLIENT_API_KEY"
  }
}
```

Use the server root URL here, **without `/v1`**. Protect the settings file because it contains the client key. On macOS/Linux:

```sh
chmod 600 ~/.claude/settings.json
```

Restart Claude Code after saving. Existing shell exports or project settings may override your user settings; remove conflicting endpoint/authentication overrides if necessary.

Test with a Claude model listed by `/v1/models`:

```sh
claude -p "Reply with OK only. Do not use tools." --model claude-haiku-5-5
```

Replace the example model if your server offers a different one. Your saved Claude login can remain on the client, but the gateway token takes precedence for these requests. Claude.ai organization connectors may be unavailable while gateway authentication is active.

Reference: [Claude Code gateway configuration](https://code.claude.com/docs/en/llm-gateway).

## Codex

On **each client machine**, store its proxy client API key in a private file. The following examples use macOS/Linux paths:

```sh
mkdir -p ~/.config/cliproxyapi
chmod 700 ~/.config/cliproxyapi
# Create client-api-key.txt with a text editor; its only content is your client API key.
chmod 600 ~/.config/cliproxyapi/client-api-key.txt
```

Edit `~/.codex/config.toml`. Put `model_provider` at the top level, before any `[table]` headers, and add the provider definition below. Preserve your existing model choice if it is available on the proxy.

```toml
model = "gpt-6.1-sol"
model_provider = "cliproxyapi"

[model_providers.cliproxyapi]
name = "CLIProxyAPI (Tailscale)"
base_url = "https://proxy-host.your-tailnet.ts.net/v1"
wire_api = "responses"

[model_providers.cliproxyapi.auth]
command = "/bin/cat"
args = ["/Users/YOUR_USERNAME/.config/cliproxyapi/client-api-key.txt"]
```

Use the actual **absolute path on that client machine** in `args`; do not copy a path from the server. On Linux it may be `/home/YOUR_USERNAME/.config/cliproxyapi/client-api-key.txt`. Codex's URL includes **`/v1`**. The token command prints only the client key, without `Bearer` or a label.

This uses Codex's command-backed bearer authentication. Do not combine it with `env_key`, `experimental_bearer_token`, or `requires_openai_auth` in the same provider definition. Keep your existing ChatGPT login; there is no need to overwrite `auth.json`.

For a Windows client, use the same provider URL and replace the authentication block with PowerShell and that machine's absolute key-file path:

```toml
[model_providers.cliproxyapi.auth]
command = "powershell.exe"
args = ["-NoProfile", "-NonInteractive", "-Command", "Get-Content -Raw 'C:/Users/YOUR_USERNAME/.config/cliproxyapi/client-api-key.txt'"]
```

Restrict that file's permissions to your user account. Install a current Codex version that supports command-backed provider authentication.

Restart the Codex CLI or desktop app, then start a new session. Test from a terminal:

```sh
codex exec --ephemeral --skip-git-repo-check "Reply with OK only. Do not use tools."
```

The startup banner should show `provider: cliproxyapi`. A completed response verifies a real Codex request through the proxy.

Reference: [Codex configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Cannot connect or resolve the hostname | Both machines are connected to Tailscale, the hostname matches `tailscale serve status`, tailnet rules permit access, and the server is awake. |
| HTTP 401 from a model endpoint | Use a client API key configured on this server. Do not use the dashboard management key. |
| Dashboard says the management key is invalid | Sign in with the separate management key, without `Bearer` or a label. |
| HTTP 429 / `rate_limit_error` | Inspect the upstream account's quota/reset time. Proxy routing does not create additional subscription quota. |
| Model not found | Pick a model ID returned by `/v1/models`; listing a model alone does not prove the account can use it. |
| Codex still connects directly | Restart it, create a new session, and check for a profile/project/command-line override of `model_provider`. |
| Claude Code still connects directly | Restart it and check conflicting environment variables or project settings. |
| Dashboard cannot display logs | Enable `observability.logs.logging-to-file` on the server; refresh the dashboard after the config is reloaded. |

On the server, the management API can confirm requests reached the proxy. Application logs and request logging must be enabled separately. Request logs can include prompt and response content; keep access restricted.

## Return to direct provider access

- **Claude Code:** remove `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` from its settings and remove corresponding shell exports, then restart it.
- **Codex:** remove the top-level `model_provider = "cliproxyapi"` setting (or select `openai`), then restart it. The unused custom provider table can remain or be removed.

Revoke a machine's proxy client API key in the server's access settings when that machine should no longer connect.
