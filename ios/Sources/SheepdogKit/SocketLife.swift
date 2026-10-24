import Foundation

/// One socket's connection lifecycle: idle (never opened, or closed for
/// good), opening, open, or backing off after a drop.
///
/// Shared by `SessionState.Inputs.socket`, the `/events` stream's life
/// (Health ▸ Live updates) and `VoiceSocket`'s own internal bookkeeping —
/// these used to be three separate near-identical enums (one of them spelling "not
/// connected" `closed` and "backing off" `waitingToRetry`), so a reader had
/// to remember which flavour a given `.idle`/`.closed` belonged to.
public enum SocketLife: String, Equatable, Sendable {
    case idle, connecting, open, reconnecting
}
