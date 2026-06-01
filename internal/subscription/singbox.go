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
	for _, ob := range remoteOuts {
		if tag, ok := ob["tag"].(string); ok && tag != "" {
			ob["tag"] = RewriteRemoteNodeName(tag, alias)
		}
		localOuts = append(localOuts, ob)
	}
	return json.Marshal(localOuts)
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
