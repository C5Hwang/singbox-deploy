package system

import (
	"bufio"
	"strings"
)

// Family is the supported OS family. Alpine is intentionally unsupported.
type Family string

const (
	FamilyDebian  Family = "debian"
	FamilyRHEL    Family = "rhel"
	FamilyUnknown Family = ""
)

// OSRelease holds the parsed, normalized contents of /etc/os-release plus the
// derived package manager.
type OSRelease struct {
	ID             string
	VersionID      string
	Family         Family
	PackageManager string
}

// ParseOSRelease parses /etc/os-release content and derives the OS family and
// package manager. RHEL-like systems use dnf; the caller may downgrade to yum
// when dnf is absent.
func ParseOSRelease(content string) (OSRelease, error) {
	vals := map[string]string{}
	s := bufio.NewScanner(strings.NewReader(content))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			vals[k] = strings.Trim(v, `"`)
		}
	}
	id := vals["ID"]
	osr := OSRelease{ID: id, VersionID: vals["VERSION_ID"]}
	switch id {
	case "ubuntu", "debian":
		osr.Family, osr.PackageManager = FamilyDebian, "apt"
	case "centos", "rocky", "almalinux", "rhel":
		osr.Family, osr.PackageManager = FamilyRHEL, "dnf"
	default:
		idLike := vals["ID_LIKE"]
		switch {
		case strings.Contains(idLike, "debian"):
			osr.Family, osr.PackageManager = FamilyDebian, "apt"
		case strings.Contains(idLike, "rhel"), strings.Contains(idLike, "centos"), strings.Contains(idLike, "fedora"):
			osr.Family, osr.PackageManager = FamilyRHEL, "dnf"
		}
	}
	return osr, s.Err()
}

// Supported reports whether the detected OS family is supported.
func (o OSRelease) Supported() bool {
	return o.Family == FamilyDebian || o.Family == FamilyRHEL
}
