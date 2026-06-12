package subscription

import "fmt"

// RemoteEntry describes a same-version remote subscription server to aggregate.
// The local program derives the remote token from Salt using the shared
// md5(salt+"\n") convention.
type RemoteEntry struct {
	Domain string
	Port   int
	Alias  string
	Salt   string
}

// Token returns the remote subscription token for this entry.
func (e RemoteEntry) Token() string { return TokenFromSalt(e.Salt) }

// base returns the https origin for this entry, including the port.
func (e RemoteEntry) base() string {
	return fmt.Sprintf("https://%s:%d", e.Domain, e.Port)
}

// DefaultURL is the remote /s/default endpoint (base64 universal links).
func (e RemoteEntry) DefaultURL() string {
	return fmt.Sprintf("%s/s/default/%s", e.base(), e.Token())
}

// ClashURL is the remote /s/clashMeta endpoint (node fragment).
func (e RemoteEntry) ClashURL() string {
	return fmt.Sprintf("%s/s/clashMeta/%s", e.base(), e.Token())
}

// SingBoxProfilesURL is the remote /s/singboxProfiles endpoint (full client profile).
func (e RemoteEntry) SingBoxProfilesURL() string {
	return fmt.Sprintf("%s/s/singboxProfiles/%s", e.base(), e.Token())
}

// SingBoxURL is the legacy remote /s/sing-box endpoint for older versions.
func (e RemoteEntry) SingBoxURL() string {
	return fmt.Sprintf("%s/s/sing-box/%s", e.base(), e.Token())
}

// SurgeURL is the remote /s/surge endpoint (Surge proxy list fragment).
func (e RemoteEntry) SurgeURL() string {
	return fmt.Sprintf("%s/s/surge/%s", e.base(), e.Token())
}
