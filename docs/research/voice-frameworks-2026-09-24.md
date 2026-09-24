# 语音层调研：问题对照、主流框架做法、WebRTC/Pion 评估

日期：2026-09-24。调研方式：只读源码和官方文档，全程没有调用 Gemini 或 OpenAI API。
上游源码克隆在调研会话的 scratchpad 中（livekit-agents、pipecat、ten-framework），没有进仓库。

标记：**[源码]** 表示亲自读过的源码（给出路径）；**[文档]** 表示官方文档原文；**[推断]** 表示尚未实测或没有一手来源。

文末的“§6 实测补充”记录了同日 18:11–18:26 UTC 那次实测的日志发现。

## 0. 摘要：前 5 条建议（按收益/成本排序）

| # | 建议 | 收益 | 成本 | 置信度 |
|---|---|---|---|---|
| 1 | GPT-Live 输入按真实时间节奏发送。`keepAlive` 改成按墙钟补足欠下的静音，或者 GPT-Live 下不对输入做闸门、改用 `session.input_audio.mute`。然后重测 `realtime_ratio`，接近 1.0 就把 1200 ms 起播缓冲降到约 300 ms | 可能从根上消除卡顿，首字延迟少约 0.9 s | 小；需要约 30–60 计费秒，先问用户 | 中高 [推断，证据强] |
| 2 | `/ws` 新增 `playback` 消息，由客户端回报实际播放位置；“Assistant said” 截断到这个位置，并静默告诉语音模型用户只听到了哪些 | brain 的指代更准 | 中 | 高 [源码+文档] |
| 3 | `Speaking()` 同时看客户端播放状态，而不只看生成状态 | 真正做到 #34 的“不在句中打断” | 小到中 | 高 [源码] |
| 4 | 回声：确认平台 AEC 已覆盖，再补盲区（手机浏览器、Safari），读 `track.getSettings()` 并提示用户 | 避免模型自我打断 | 小 | 桌面 Chrome 高；其余 [未验证] |
| 5 | push 定时改用客户端 VAD。现在“用户在说话”= 1.5 s 内有输入转写，有滞后 | 主动播报更少撞上用户开口 | 小 | 中 [推断] |

WebRTC/Pion 结论：现在不迁移，保留 `/ws`。等 1–3 做完、重新度量后，只对浏览器做 spike。

## 1. 问题对照表

| # | 症状 | 当前修复 | 代码位置 | 仍然脆弱在哪里 |
|---|---|---|---|---|
| P1 | GPT-Live 播放卡顿 | 自适应抖动缓冲（#43）、起播 1200 ms（#46）、WSOLA 变速 0.85–1.05（#45） | `web/voice/audio-util.js` `Player`/`Wsola`；iOS `PlaybackQueue` + `AVAudioUnitTimePitch` | 治标。输出只有 0.74–0.87× 实时，首字多等 1.2 s。疑似根因在输入侧（见下） |
| P2 | 声学回声导致自我打断 | web：`getUserMedia({echoCancellation:true})`（`web/voice/app.js:207`）；iOS：`.voiceChat` + `setVoiceProcessingEnabled(true)`（`AudioEngine.swift:137-142`） | 同左 | iOS 真机未验证；手机浏览器、Safari 未验证；GPT-Live 没有 VAD 可调，回声漏过就会触发打断 |
| P3 | 模型把自己的 push 转回 `to_supervisor` | `isEcho` 改为整词、≥4 词、覆盖 ≥60%（#48） | `internal/supervisor/utterance.go:493` | 靠文本启发式；iteration-2 WP3（#58）已移除这个 guard，见 §6 |
| P4 | push/工具结果切断模型说话 | 说话时 INTERRUPT 降级为 WHEN_IDLE（#34） | `gemini/push.go`、`toolexec.go:83` | `Speaking()` 看的是生成状态，`turnComplete` 时就置 false（`gemini/reader.go`）。Gemini 生成快约 4 倍，turnComplete 后客户端还在播，这时来的 INTERRUPT push 会 `interrupt("push")` 并 flush 客户端 [源码] |
| P5 | “Assistant said” 不等于实际播出的内容 | 无 | `supervisor.OnVoiceEvent` → `brain.Said` → `flushOutLocked`（`internal/brain/brain.go`） | 被打断时 brain 以为用户听完了。Gemini 文档：“Only the information already sent to the client is retained in the session history”，而 daemon 早已收齐整段。GPT-Live 上下文只能追加 |
| P6 | “用户在说话”的判断滞后 | `SpeechHold` 1.5 s（按输入转写） | `gemini/push.go:156-162` | 转写本身有滞后 |
| P7 | 静音流让会话退化、回合卡死 | 静音闸门 + `audioStreamEnd` + 舒适噪声；停滞回收（#16） | `gemini/turn.go`、`session.go` | 对 Gemini 合理；对 GPT-Live 适得其反（P1） |
| P8 | 1011 下带 handle 的恢复一直失败 | 1011 时丢弃 handle 重新连接（#41）；重连期间 push 不丢（#48） | `gemini/errors.go`、`run.go` | 靠错误文本匹配；已经比 LiveKit/Pipecat 更细 |
| P9 | brain 慢 | `reply_wait_s`（2 s）后转为迟到 push（#47） | supervisor、`gptlive/tools.go` | 丢弃规则较复杂 |

**P1 根因假设：**

1. OpenAI 文档要求 “supply a continuous microphone stream paced at its recorded sample rate”，并说明 “If the session timeline stops, the acknowledgment can remain pending” [文档 `guides/voice-websockets?api=live`、`live-conversations`]。
2. 客户端闸门关闭时（用户不说话，正好是模型回复的时候），`keepAlive` 每 40 ms tick 一次，距上次发送不足 80 ms 就跳过，否则发 40 ms。所以输入时间线最多 0.5×（tick 抖动时 0.33×）[源码 `internal/voice/gptlive/loop.go:42-53`，编排会话已复核]。
3. 实测输出 0.74–0.87×；Gemini 不依赖输入推进，是 4 倍速。
4. 对照：
   - LiveKit 注释写输出 “arrives at playback pace”（`llm/duplex_adapter.py`），console 预缓冲 `PREBUFFER_DURATION = 0.3`，理由是 “largest arrival gap observed with GPT-Live”（`cli/_legacy.py`）。
   - LiveKit 静音用 `session.input_audio.mute`（`gpt_live_model.py` `mute_input`）。
   - Pipecat 把连续输入原样 append（`services/openai/live/llm.py`）[源码]。

## 2. 各框架要点

### 版本与成熟度（2026-09-24 通过 GitHub API 核实）

| 项目 | 最新发布 | 许可证 | 语言 | 活跃度 |
|---|---|---|---|---|
| LiveKit agents | 1.8.2（09-15） | Apache-2.0 | Python | 当天有提交 |
| agents-js | 1.9.x（09-15） | Apache-2.0 | TS | 活跃 |
| client-sdk-swift | 2.17.0（09-14） | Apache-2.0 | Swift | 活跃 |
| Pipecat | v1.11.0（09-18） | BSD-2 | Python | 活跃 |
| pipecat-client-ios | 没有 release，最后 push 07-02 | BSD-2 | Swift | 较冷 |
| TEN | 0.11.73（09-22） | Apache-2.0 + 附加条款 | C 核心 + Go/Py/Node 绑定 | 活跃 |
| openai-agents-python | v0.22.3（09-17） | MIT | Python | 活跃 |
| openai-agents-js | v0.18.0（09-10） | MIT | TS | 活跃 |
| pion/webrtc | v4.2.21（09-24） | MIT | Go | 非常活跃 |
| pion/opus | v0.1.0（06-08），编码和解码都有 | MIT | Go | 活跃，刚到 0.1 |
| stasel/WebRTC | 153.0.0（09-11） | 上游 BSD | 预编译二进制 | 跟随 Chromium |

TEN 的 `LICENSE` 原文禁止 “host … on any End User devices” 以及与 Agora 竞争的部署。daemon 跑在用户自己的 Mac 上，很可能落在限制内，所以不要引入 TEN 代码。`ten-vad` 没有终端设备条款，但有竞争条款 [源码]。

**LiveKit 和 Pipecat 都已实现 GPT-Live 的 client delegation**，与 sheepdog 是同一架构，是最直接的参考 [源码]：
- LiveKit：`livekit-plugins-openai/.../realtime/gpt_live_model.py`，示例 `examples/voice_agents/gpt-live/client_delegation.py`
- Pipecat：`pipecat/services/openai/live/llm.py`，示例 `examples/realtime/realtime-openai-live-client-delegation*.py`

### 抖动
- LiveKit：`rtc.AudioSource(queue_size_ms=200)`，接收端交给 NetEQ。
- Pipecat：`base_output.py` 的 `_audio_runs` 固定节奏切块。
- OpenAI：客户端推荐用 WebRTC；WS 下没有时间戳。
- sheepdog 客户端的 WSOLA 已经比两个框架都精细，要修的是源头。

### AEC
- **桌面 Chrome：** Chromium `media/base/media_switches.cc` 里 `kChromeWideEchoCancellation` 为 FEATURE_ENABLED_BY_DEFAULT，注释是 “mix and cancel all audio playback going to a specific output device”；`media_options.gni` 只在 Win/Mac/Linux 编译。所以 AudioWorklet 的输出也是 AEC 参考 [源码]。discuss-webrtc 2023-06 的实测结论一致（gain=0 不算）。
- **其他平台：** 旧说法“Web Audio 不进 AEC”（crbug 40504498）适用于没有 chrome-wide AEC 的平台（Android、iOS 上的 WebKit）[推断；bug 页需要登录]。Safari 未验证。
- **iOS：** VPIO 已开；iOS 17 有 `voiceProcessingOtherAudioDuckingConfiguration`（经搜索转引）。
- **Gemini 文档：** 建议 “Use headphones … prevent the model from interrupting itself”。
- **框架本身：** LiveKit/Pipecat 自身没有 AEC 代码，依赖客户端 WebRTC APM；LiveKit 有 Krisp 降噪插件。

### 打断
- **LiveKit：** `SpeechHandle.allow_interruptions`，误打断恢复（`_start_false_interruption_timer`，`agent_activity.py`）。GPT-Live 适配器的 `interrupt()` 为空，注释 “barge-in is the model's own”。
- **Pipecat：** `InterruptionFrame` 加逐帧 `interruptible` 标志；GPT-Live 推荐 `enable_interruptions=False`，工具一律 `cancel_on_interruption=False`。
- **Gemini：** `interrupted` 字段；`activityHandling: NO_INTERRUPTION`。
- **GPT-Live：** `instructions.append` 可以打断；要挡住音频只能在客户端或中继控制播放 [文档 `voice-server-controls?api=live`]。
- **借鉴：** push 带 `interruptible:false`，客户端只清掉可打断的段。

### 转写截断
- **LiveKit：** `playback_position` 加 `synchronized_transcript`，chat_ctx 记录 `forwarded_text`（`agent_activity.py:~3297-3347`）。OpenAI Realtime 走 `truncate(audio_end_ms)`；Google 插件 `message_truncation=False`（`realtime_api.py:354`），只修框架自己的上下文。
- **Pipecat：** `_truncate_current_audio_response` 取 `min(已播墙钟, 音频时长)`。
- **Agents SDK：** `RealtimePlaybackTracker`（`on_play_bytes`/`on_play_ms`）。
- **GPT-Live：** 无法截断。输出转写带 `start_ms/end_ms`，文档说 “Discarded speech remains in the model's conversation context”。
- **Gemini：** 没有截断 API。
- **做法：** 照 LiveKit 截断自己的上下文，再用静默说明告知语音模型。

### 轮次/VAD
- LiveKit turn-detector（ONNX）；Pipecat Silero 加 Smart Turn v3（含 CoreML）；TEN VAD 和 Turn Detection（Qwen2.5-7B）。
- Gemini 的 `automaticActivityDetection` 可调；GPT-Live 没有任何 VAD 参数。
- sheepdog 不需要 turn detector，客户端 VAD 只用于 push 定时和懒开会话。

### 重连
- **LiveKit Google 插件：** GoAway 只记日志，并留着 `TODO … isn't a seamless reconnection`。
- **Pipecat：** 不特殊处理 goAway，重连后强制推理一次。
- **Gemini：** handle 有效 2 小时，可用上下文压缩突破时长上限。
- **GPT-Live：** 没有恢复，文档建议闲时关闭再用历史重新 seed，并缓冲开头语音。sheepdog 已经这样做。
- **结论：** sheepdog 在这方面持平或领先，不需要改。

### 填充/异步
- LiveKit 的 `BackgroundAudioPlayer` 播思考音效。
- Pipecat 用 `cancel_on_interruption`/`run_llm`（v1.11.0 PR #5820）。
- Gemini 有 NON_BLOCKING 加 scheduling。
- GPT-Live 的 delegation 天然异步。
- 可选：等 brain 时在客户端本地播轻微的思考音效，不进入模型和转写。

### 主动播报
- LiveKit 有 `min_consecutive_speech_delay` 和误打断恢复。
- GPT-Live 用 `commentary.append`，由模型决定何时说。
- Gemini 的 WHEN_IDLE 按生成状态判断，而不是播放状态。
- sheepdog 的缺口是 P4 和 P6。

## 3. WebRTC/Pion 评估

### 方案 A：客户端 ↔ daemon 走 Pion，daemon 仍用 WS 连模型

**能消除：** 手机浏览器和 Safari 的 AEC 问题（接收轨道挂 `<audio>`）、远程 TCP 队头阻塞、客户端抖动（NetEQ）。

**不能消除：** 模型端欠速。daemon 的 20 ms pacer 仍会饿，欠载只是从客户端挪到了 daemon。截断仍要自己做（用 `getSynchronizationSources`、`jitterBufferDelay`）。

**实现要点：**
- `pion/opus` 是纯 Go 编解码（MIT，v0.1.0，成熟度需要自测），备选 cgo 的 `hraban/opus`；需要做 48↔16/24 kHz 重采样。
- 信令：`POST /rtc/offer`（同一套 Bearer 鉴权），等 ICE 收集完成（不用 trickle），参考 Pion 的 `whip-whep` 示例。
- `SetICEUDPMux` 单 UDP 端口。LAN 和 Tailscale 靠 host candidate 应该能通 [推断]；公网访问要 `pion/turn`，不建议，定位为“用 Tailscale”。
- 浏览器：`RTCPeerConnection` 加 `<audio>`，DataChannel 复用 wsproto，worklet 留给 `/ws` 回退。
- iOS：没有第一方 WebRTC，只能用 `stasel/WebRTC` 或 LiveKit 的 xcframework（三方二进制），所以 iOS 保留 `/ws` 和 VPIO。

**工作量与风险：** 约 3–4 周，拆成 4–5 个 PR [推断]。风险包括 pion/opus 成熟度、Go timer 抖动、多一个端口的攻击面、测试量翻倍。

### 方案 B：客户端直连 OpenAI WebRTC + daemon sideband（仅 GPT-Live）

- **做法：** daemon 带 key 调 `POST /v1/live/sessions`（transport webrtc），再连 `wss://api.openai.com/v1/live/sessions/{id}/attach`，在 sideband 上收 delegation、发 `commentary/thinking/instructions.append`；sideband 还会收到带 `start_ms/end_ms` 的反射音频 [文档 `voice-webrtc?api=live`、`voice-server-controls?api=live`]。
- **能消除：** GPT-Live 的节奏和抖动问题、浏览器 AEC 问题。
- **代价：**
  - Gemini 没有官方 WebRTC（只有第三方集成），要维护两套传输；
  - 懒开会话和预卷重放变难；
  - 每次建会话预扣 15 s（运行后抵扣）；
  - iOS 需要 WebRTC 库。
- **建议：** 等方案 C 实测后再决定。

### 方案 C（推荐）：WS + 平台 AEC + 修根因

- 桌面 Chrome 已够 [源码]。
- iOS 理论上够，但 §6 的实测显示回声仍会漏过，需要真机排查（用 `--voice=off` 就不耗 API 额度）。
- 手机浏览器和 Safari 不确定，界面提示用户用耳机或 app。

## 4. 未验证

- crbug 40504498 现状；Safari/WebKit 的 AEC 行为。
- P1 假设需要一次计费实测；也要看 audio-e2e 当时输入是否处于闸门关闭状态。
- Apple VPIO 文档细节（经搜索转引）。
- stasel 和 LiveKit xcframework 的 README 细节。
- pion/opus 编码音质和 CPU 开销。
- Tailscale 上 host candidate 的实际连通性。

## 5. 工作包（每项可以单独提 PR）

- **WP-A** `fix(voice): pace GPT-Live input at real time while the client is silent`
  - 文件：`internal/voice/gptlive/loop.go`（keepAlive 按“已发时长 vs 墙钟”补足欠额，超过 200 ms 的欠额丢弃）、`session.go`（sendAudio 累计时长）、`session_test.go`（假时钟：10 s 墙钟发出约 10 s 音频）。可选改用 `input_audio.mute`。
  - 验证：先离线单测；再经用户同意跑一次 `audio-e2e --audio-timeline`（GPT-Live）。
  - 后续小 PR：`docs/ws-contract.md`、`web/voice/audio-util.js`、iOS `PlaybackProfile` 把 GPT-Live 起播缓冲降到约 300–400 ms。
- **WP-B** `feat(ws): clients report playback state and played position`
  - 契约：客户端→服务端 `{"type":"playback","state":"started|drained|flushed","played_ms":N}`，flush 时必须回报。
  - 文件：`docs/ws-contract.md`、`internal/voice/wsproto/wsproto.go`、`internal/web/ws.go`、`web/voice/audio-util.js`、`web/voice/app.js`，加测试；iOS 仓库另提 PR。
- **WP-C** `feat(brain): record what the shepherd actually heard`
  - Gemini：`gemini/reader.go` 记录每段转写到达时已收的音频字节，打断时按 `played_ms` 截断。GPT-Live：用 `start_ms/end_ms` 对齐。
  - 数据：`voice.EventTranscript` 增加截断字段；`brain/brain.go` 写入截断文本；`brain/assemble.go` 渲染为 “Assistant said (interrupted)”。
  - 可选：静默告知语音模型。
- **WP-D** `fix(voice): a reply still playing on the client counts as speaking`
  - 文件：`internal/voice/voice.go`、`switch.go`、`gemini/session.go`、`gemini/push.go`（PushGap 从 drained 开始计时）、`gptlive/session.go`、`web/ws.go`，加测试。
- **WP-E** `feat(voice-web): warn when echo cancellation is unavailable`
  - 文件：`web/voice/app.js`（`getSettings`、平台提示）、ping 增加 `aec` 字段、`docs/ws-contract.md`。
  - iOS 真机回声测试列入 iOS 仓库的 `docs/status.md`。
- **WP-F** `feat(voice): use the client's speech detector for push timing`
  - 契约：`{"type":"speaking","on":bool}`。
  - 文件：`docs/ws-contract.md`、wsproto、`web/ws.go`、`gemini/push.go`、gptlive 对应位置。
- **WP-G（可选 spike）** 浏览器 WebRTC
  - 文件：`internal/web/rtc/`、`POST /rtc/offer`、`web/voice/rtc.js`、`CONFIG.md`。
  - 退出标准：手机浏览器 AEC 或远程欠载有明显改善，否则删掉。
  - 或者：先为方案 B 写一页设计说明，再决定做不做。

顺序：A → 实测 → B → C 与 D（文件不重叠，可并行）→ E、F → 视数据决定是否做 G。

## 6. 实测补充（2026-09-24 18:11–18:26 UTC，daemon `d0f79b9`）

数据来源：`~/.local/share/sheepdog/events/2026-09-24.jsonl`，以及 brain 请求快照 `brain/20260924T181415Z.requests.jsonl`。
会话顺序：web GPT-Live（18:14–18:17）→ web Gemini（18:21–18:22）→ iOS Gemini（18:23–18:24）→ iOS GPT-Live（18:24–18:25）。brain 用的是 DeepSeek `deepseek-flash`。

### 6.1 深入追问必然失败：brain 请求的消息顺序错误（最高优先级）

- **现象：** 共有 5 个 brain turn 以 `deepseek: HTTP 400: An assistant message with 'tool_calls' must be followed by tool messages` 失败（turn 6、7、8、14、15）。每一次都是你在追问细节（“glitch 的细节”“claude-3 具体说了什么”“我说的是某个具体的 agent”）。
- **根因（已在请求快照中确认）：**
  - brain 调用 `read_screen` 的同时，语音模型说出了占位语 “On it.”；
  - 这句 `Assistant said` 被追加到 transcript 中，恰好落在 tool call 和 tool result 之间；
  - 拼装出的顺序变成 `assistant(tool_calls) → user("Assistant said: On it.") → tool`，违反了 OpenAI 兼容 API 的约束。
  - 触发时机：`reply_wait_s = 2 s` 到点后，语音模型正好开口说占位语，这与 brain 第一轮工具调用的时间高度重合。
- **后果：**
  1. 这个 turn 退回到代码生成的兜底句 “No change since I last checked: Four agents: …”，答非所问；
  2. 错误顺序留在窗口里，之后**每一个** turn（包括 stop 和 finish 播报）都失败，直到 “New conversation” 清空窗口；
  3. 18:18 的 finish 播报因此只剩 “herdr-sheepdog-claude finished.”。
- **结论：** 这是“只能做简单状态汇报、无法深入”最直接的原因。**深入追问 = 要读屏幕 = 撞上这个 bug。**
- **修复方向：**
  - 拼装时把落在 tool call 和 result 之间的上下文消息挪到 result 之后，Gemini 的 `toContents` 同样处理；
  - 或者在一个 step 执行期间先缓存旁路消息，step 结束后再写入 transcript；
  - 再加一条回归测试。

### 6.2 “No change since I last checked” 兜底句

- 另一编排会话已经发现：失败的 turn 之后，第一次状态回答也会带上这个前缀。
- 这次它出现在 5 次失败中的 3 次，而且读的是机器腔的名册句子。
- 修复：兜底回答不应该标记“已汇报”，也不该用重复前缀。

### 6.3 web 端 Gemini “突然沉默”

- **第一次沉默（18:21:28–18:21:44），是浏览器播放卡住：**
  - 客户端统计显示 `buffer_ms` 连续 15 s 停在 **3155 ms**，`frames_out` 不再增加，`rate 1`；
  - 也就是说，音频已经送到了浏览器，但播放器没有继续消费。Gemini 本身正常（`realtime_ratio` 7.9，最大间隔 5.6 ms）；
  - 疑点：从 GPT-Live 切到 Gemini 后，播放器状态（起播目标、WSOLA）没有复位，或者 AudioContext 被挂起。需要在浏览器里复现。
- **之后的回答发给了空气：**
  - 你重连了两次，18:21:54 页面关闭；
  - 18:21:57 的问题（音频在断开前已经送到 Gemini）在 18:22:04 得到回答，12 s 的回复在**没有任何客户端连接**的情况下播完。
  - daemon 应该在没有客户端时暂缓 push，或者在重连后补播。

### 6.4 iOS Gemini 的自我打断（回声）

- 18:23:50 模型开始说 “No change, all four are idle…”；
- 18:23:51.4 客户端静音闸门打开，18:23:51.9 Gemini 判定用户插话（`voice.interrupted by:model`）；
- 18:23:54 转写出 “New change”，其实就是它自己那句 “No change” 的回声，随后被当作一次新的用户输入交给 brain。
- 结论：
  - iOS 上 VPIO 的回声消除没有挡住外放；
  - 而 #58 移除了文本回声 guard，所以回声现在会直接变成一次用户发言；
  - P2 在 iOS 上不是“未验证”，而是**已复现**。

### 6.5 正面信号

- **web GPT-Live 表现可以接受：** `rate` 大多为 1，有 12 次 drain。与 P1 的“输入节奏”假设一致：web 页面持续发送麦克风音频，闸门关闭的时间比 iOS 少。
- **播放被打断的情况好转：** 本次只看到 1 次 `voice.interrupted by:push`，发生在 stop 时，属于预期行为。
