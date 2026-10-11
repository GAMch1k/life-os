package backup

import "context"

// Target keeps another copy of the completed runs. The backup directory
// (Store) always holds the first copy and is the only target implemented;
// the encrypted off-site copy planned for P14-04 is meant to be a Target.
//
// The runner gives each target every completed run, after the run is
// complete on disk and while it still holds the backup lock, and passes on
// the runs the rotation deletes, so a target mirrors what the backup
// directory keeps. A target that fails does not fail the run: the failure
// is logged and recorded in the run history.
type Target interface {
	// Name names the target in logs and the history, such as "r2".
	Name() string
	// Put copies the completed run whose files (manifest.json and the
	// dumps) are in dir.
	Put(ctx context.Context, manifest Manifest, dir string) error
	// Delete removes a run the rotation deleted from the backup directory.
	Delete(ctx context.Context, id string) error
}

// Copy is how putting a run on one target went.
type Copy struct {
	Target string `json:"target"`
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}
