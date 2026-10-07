// Package push sends Apple Push Notification service (APNs) alerts to the
// iOS companion app when an agent needs the user: a blocked agent or a
// pending approval prompt, and a promise the user asked for ("tell me
// when X finishes"). The app registers its device token over the web API
// (POST /api/push/register); the supervisor hands each announced event to
// Sender.Notify once, and the Sender fans it out to every registered
// device (docs/push.md).
//
// Only the standard library is used: net/http speaks HTTP/2 to APNs over
// TLS on its own, and the provider token (an ES256 JWT) is signed here
// with crypto/ecdsa and refreshed every 50 minutes.
package push
