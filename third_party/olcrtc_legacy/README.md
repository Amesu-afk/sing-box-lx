# olcRTC legacy Android runtime

This is the minimal client-side subset of `Oleglog/Olcrtc_manager` at commit
`c267dd30b0bc`, retained for compatibility with manager QR profiles that use
the version 1 handshake and legacy encrypted record format.

Only the packages required by the Android mobile binding are included. The
mobile facade and provider registration are intentionally limited to the
Jitsi/datachannel and VP8 client paths used by TarnVPN; server, database, CLI,
and bundled binary assets are omitted.

The upstream license is preserved in `LICENSE`.
