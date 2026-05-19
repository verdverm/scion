# Control Channel Max Message Size Config

## Problem
The control channel WebSocket has a hardcoded 64KB max message size limit. This causes `scion exec` to fail for commands that produce large output (e.g., `opencode export` produces 340KB+), with error: `control channel request failed: connection closed (status: 502)`.

## Current State
- `pkg/hub/controlchannel.go:55` — `DefaultControlChannelConfig()` hardcodes `MaxMessageSize: 64 * 1024`
- `pkg/hub/server.go:669` — Hub server hardcodes `MaxMessageSize: 64 * 1024` in `NewControlChannelManager()` call
- `pkg/wsprotocol/connection.go:35` — `DefaultMaxMessageSize = 64 * 1024`
- `pkg/runtimebroker/controlchannel.go:231` — Broker calls `wsprotocol.Dial()` which hardcodes `DefaultConnectionConfig()`, no config field to override
- No config option exists to override this

## Impact
- `scion exec` works for small commands (`echo`, `which`) but fails for large output (`opencode export`, large `ls -la`, etc.)
- The 502 error is "connection closed" — the WebSocket rejects messages over the read limit

## Proposed Solution
Add a `control_channel` section to `V1ServerConfig` in `settings.yaml`:

```yaml
server:
  control_channel:
    max_message_size: 10485760  # 10MB, default 64KB if omitted
```

### Changes needed

#### Hub side
1. **`pkg/config/settings_v1.go`** — Add `V1ControlChannelConfig` struct with `MaxMessageSize` field (int64 or string like "10MB") to `V1ServerConfig`
2. **`pkg/hub/server.go`** — Read the config value and pass it to `ControlChannelConfig{}` instead of hardcoding
3. **`pkg/config/settings_v1.go`** — Wire the conversion in `ConvertGlobalToV1ServerConfig` and `ConvertV1ServerToGlobalConfig` if a `GlobalConfig` equivalent is needed

#### Broker side
4. **`pkg/runtimebroker/controlchannel.go:37`** — Add `MaxMessageSize int64` field to `ControlChannelConfig`
5. **`pkg/runtimebroker/controlchannel.go:231`** — Switch from `wsprotocol.Dial()` to `wsprotocol.DialWithConfig()` using the config value
6. **`pkg/runtimebroker/controlchannel.go:64`** — Set default in `DefaultControlChannelConfig()`

### Notes
- 10MB is reasonable for `opencode export` and similar commands
- Config should be global-level only (like other server settings)
- Both hub and broker sides need this config — the broker is the WebSocket reader, so increasing the hub's write limit alone won't help if the broker still rejects large messages

### Notes
- 10MB is reasonable for `opencode export` and similar commands
- Config should be global-level only (like other server settings)
- The runtime broker also has a `ControlChannelConfig` — ensure the broker side can handle large messages too (it reads from the hub, so it may need its own config)
