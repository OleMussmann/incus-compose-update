// Package incus reads instance and volume state and takes volume snapshots,
// through the Incus client library.
package incus

import (
	"os"
	"strings"
	"sync"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
)

// Instance is the part of an instance the updater looks at.
type Instance struct {
	Name    string
	State   string // upper-case, as `incus list` prints it: RUNNING, STOPPED, ...
	Running bool
	Config  map[string]string
}

// Client is what the updater needs from Incus.
type Client interface {
	Instances(project string) ([]Instance, error)
	Instance(project, name string) (Instance, error)
	CustomVolumes(project, pool string) ([]string, error)
	VolumeExists(project, pool, volume string) (bool, error)
	// Snapshot takes a snapshot that never expires: a rollback point has to
	// outlive the volume's own snapshot retention.
	Snapshot(project, pool, volume, name string) error
}

// Server is the Client backed by the Incus API. It connects on first use,
// to the remote the incus CLI would use: $INCUS_REMOTE if set (as
// incus-compose honours it), else the CLI config's default remote.
type Server struct {
	once sync.Once
	srv  incusclient.InstanceServer
	err  error
}

func (s *Server) conn(project string) (incusclient.InstanceServer, error) {
	s.once.Do(func() {
		conf, err := cliconfig.LoadConfig("")
		if err != nil {
			s.err = err
			return
		}
		remote := os.Getenv("INCUS_REMOTE")
		if remote == "" {
			remote = conf.DefaultRemote
		}
		s.srv, s.err = conf.GetInstanceServer(remote)
	})
	if s.err != nil {
		return nil, s.err
	}
	return s.srv.UseProject(project), nil
}

func toInstance(i api.Instance) Instance {
	return Instance{
		Name:    i.Name,
		State:   strings.ToUpper(i.Status),
		Running: i.StatusCode == api.Running,
		Config:  i.Config,
	}
}

// Instances implements Client.
func (s *Server) Instances(project string) ([]Instance, error) {
	c, err := s.conn(project)
	if err != nil {
		return nil, err
	}
	list, err := c.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, err
	}
	out := make([]Instance, len(list))
	for i, inst := range list {
		out[i] = toInstance(inst)
	}
	return out, nil
}

// Instance implements Client.
func (s *Server) Instance(project, name string) (Instance, error) {
	c, err := s.conn(project)
	if err != nil {
		return Instance{}, err
	}
	inst, _, err := c.GetInstance(name)
	if err != nil {
		return Instance{}, err
	}
	return toInstance(*inst), nil
}

// CustomVolumes implements Client.
func (s *Server) CustomVolumes(project, pool string) ([]string, error) {
	c, err := s.conn(project)
	if err != nil {
		return nil, err
	}
	vols, err := c.GetStoragePoolVolumes(pool)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, v := range vols {
		if v.Type == "custom" {
			out = append(out, v.Name)
		}
	}
	return out, nil
}

// VolumeExists implements Client.
func (s *Server) VolumeExists(project, pool, volume string) (bool, error) {
	c, err := s.conn(project)
	if err != nil {
		return false, err
	}
	if _, _, err := c.GetStoragePoolVolume(pool, "custom", volume); err != nil {
		if api.StatusErrorCheck(err, 404) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Snapshot implements Client. A zero ExpiresAt is how the incus CLI's
// --no-expiry asks for no expiry; leaving it nil would apply the volume's
// snapshots.expiry.
func (s *Server) Snapshot(project, pool, volume, name string) error {
	c, err := s.conn(project)
	if err != nil {
		return err
	}
	op, err := c.CreateStoragePoolVolumeSnapshot(pool, "custom", volume, api.StorageVolumeSnapshotsPost{
		Name:      name,
		ExpiresAt: &time.Time{},
	})
	if err != nil {
		return err
	}
	return op.Wait()
}
