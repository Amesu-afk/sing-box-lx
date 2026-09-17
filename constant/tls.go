package constant

const ACMETLS1Protocol = "acme-tls/1"

const (
	TLSEngineDefault = ""
	TLSEngineGo      = "go"
	TLSEngineApple   = "apple"
	TLSEngineWindows = "windows"
)

// Values of `tls.reality.key_share` — which key exchange the REALITY ClientHello
// offers. Empty = whatever the utls fingerprint carries. lx: SPEC 089.
const (
	RealityKeyShareDefault   = ""
	RealityKeyShareHybrid    = "hybrid"
	RealityKeyShareClassical = "classical"
)
