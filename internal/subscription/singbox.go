package subscription

import "encoding/json"

// MergeSingBoxOutbounds appends remote sing-box outbounds to the local set,
// rewriting each remote outbound's "tag" (the node name) with the local alias.
// Both inputs are JSON arrays of outbound objects; the result is a JSON array.
func MergeSingBoxOutbounds(local, remote []byte, alias string) ([]byte, error) {
	localOuts, err := decodeOutbounds(local)
	if err != nil {
		return nil, err
	}
	remoteOuts, err := decodeOutbounds(remote)
	if err != nil {
		return nil, err
	}
	for _, ob := range renameOutbounds(remoteOuts, alias) {
		localOuts = append(localOuts, ob)
	}
	return json.Marshal(localOuts)
}

// ExtractSingBoxNodeOutbounds extracts supported proxy outbounds from a full
// sing-box client profile. If the input is already an outbound array, it is
// filtered directly. Selectors and direct/block outbounds are excluded.
func ExtractSingBoxNodeOutbounds(profile []byte) ([]byte, error) {
	outs, err := decodeOutbounds(profile)
	if err == nil {
		return json.Marshal(filterNodeOutbounds(outs))
	}
	var root struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(profile, &root); err != nil {
		return nil, err
	}
	return json.Marshal(filterNodeOutbounds(root.Outbounds))
}

// RenameSingBoxOutbounds rewrites tags in a JSON outbound array.
func RenameSingBoxOutbounds(outbounds []byte, alias string) ([]byte, error) {
	outs, err := decodeOutbounds(outbounds)
	if err != nil {
		return nil, err
	}
	return json.Marshal(renameOutbounds(outs, alias))
}

func filterNodeOutbounds(outbounds []map[string]any) []map[string]any {
	var filtered []map[string]any
	for _, ob := range outbounds {
		typeName, _ := ob["type"].(string)
		if Supported(typeName) {
			filtered = append(filtered, ob)
		}
	}
	return filtered
}

func renameOutbounds(outbounds []map[string]any, alias string) []map[string]any {
	for _, ob := range outbounds {
		if tag, ok := ob["tag"].(string); ok && tag != "" {
			ob["tag"] = RewriteRemoteNodeName(tag, alias)
		}
	}
	return outbounds
}

func decodeOutbounds(b []byte) ([]map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var outs []map[string]any
	if err := json.Unmarshal(b, &outs); err != nil {
		return nil, err
	}
	return outs, nil
}
