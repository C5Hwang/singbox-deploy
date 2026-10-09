package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/release"
)

// hubCoreDownloadIdle aborts a Hub-side archive download that stops
// receiving data, instead of leaving the operator's add-node run hanging.
const hubCoreDownloadIdle = 60 * time.Second

// coreArchives fetches each Hub-pushed sing-box archive at most once per
// operation, so a fleet core change (and its rollback) downloads one archive
// per release and architecture however many spokes need it pushed.
type coreArchives struct {
	fetch func(ctx context.Context, tag, arch string) (nodeapi.CoreStageRequest, error)
	byKey map[string]nodeapi.CoreStageRequest
}

func (c *Controller) newCoreArchives() *coreArchives {
	return &coreArchives{fetch: c.FetchCoreArchive, byKey: map[string]nodeapi.CoreStageRequest{}}
}

func (a *coreArchives) get(ctx context.Context, tag, arch string) (nodeapi.CoreStageRequest, error) {
	key := tag + "/" + arch
	if req, ok := a.byKey[key]; ok {
		return req, nil
	}
	req, err := a.fetch(ctx, tag, arch)
	if err != nil {
		return nodeapi.CoreStageRequest{}, err
	}
	a.byKey[key] = req
	return req, nil
}

// stageSpokeCore makes the exact sing-box release available on the spoke
// before an install or core change consumes it. The spoke downloads it from
// GitHub itself first; when that fails — GitHub is unreachable from mainland
// China — the Hub downloads and verifies the archive and pushes it over the
// overlay, where the Agent re-verifies it against the upstream digest.
func (c *Controller) stageSpokeCore(ctx context.Context, node nodes.Node, tag string, archives *coreArchives, log io.Writer) error {
	alias := node.EffectiveAlias()
	client := c.NewClient(node)
	fmt.Fprintf(log, "staging sing-box %s on %s...\n", tag, alias)
	directErr := client.StageCore(ctx, nodeapi.CoreStageRequest{SingBoxVersion: tag}, log)
	if directErr == nil {
		return nil
	}
	var statusErr *nodeapi.StatusError
	if errors.As(directErr, &statusErr) && statusErr.Code == http.StatusNotImplemented {
		// An Agent predating staging downloads the release itself during the
		// operation, exactly as before.
		fmt.Fprintf(log, "%s cannot stage sing-box releases; it will download %s itself\n", alias, tag)
		return nil
	}
	if ctx.Err() != nil {
		return directErr
	}
	fmt.Fprintf(log, "%s could not download sing-box %s from GitHub: %v\n", alias, tag, directErr)
	if node.Arch == "" {
		return fmt.Errorf("stage sing-box %s on %s: %w (the Hub cannot push it because the spoke architecture is unknown)", tag, alias, directErr)
	}
	fmt.Fprintf(log, "pushing sing-box %s to %s from the Hub over WireGuard...\n", tag, alias)
	req, err := archives.get(ctx, tag, node.Arch)
	if err != nil {
		return fmt.Errorf("stage sing-box %s on %s: spoke download failed (%v) and the Hub download failed: %w", tag, alias, directErr, err)
	}
	if err := client.StageCore(ctx, req, log); err != nil {
		return fmt.Errorf("push sing-box %s to %s: %w", tag, alias, err)
	}
	return nil
}

// fetchCoreArchive downloads and verifies one upstream sing-box archive on the
// Hub and packs it, with the metadata it was verified against, for a push.
func fetchCoreArchive(ctx context.Context, tag, arch string) (nodeapi.CoreStageRequest, error) {
	dir, err := os.MkdirTemp("", "singbox-deploy-core-")
	if err != nil {
		return nodeapi.CoreStageRequest{}, err
	}
	defer os.RemoveAll(dir)
	download := release.NewStallGuardedDownloader(hubCoreDownloadIdle)
	archive, err := release.FetchSingBoxArchive(ctx, download, tag, "linux", arch, dir)
	if err != nil {
		return nodeapi.CoreStageRequest{}, err
	}
	body, err := os.ReadFile(archive.Path)
	if err != nil {
		return nodeapi.CoreStageRequest{}, err
	}
	metadata, err := os.ReadFile(archive.MetadataPath)
	if err != nil {
		return nodeapi.CoreStageRequest{}, err
	}
	return nodeapi.CoreStageRequest{
		SingBoxVersion:  tag,
		ArchiveName:     archive.Name,
		ArchiveSHA256:   archive.SHA256,
		Archive:         body,
		ReleaseMetadata: metadata,
	}, nil
}
