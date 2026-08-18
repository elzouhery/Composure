package diagnose

import "errors"

// The only errors this package produces. Both mean "there was nothing to
// diagnose", which is the sole condition AD-13 allows to leave here as an
// error: a configuration that is merely wrong is findings.
var (
	ErrNoProject = errors.New("diagnose: no resolved project")
	ErrNoGraph   = errors.New("diagnose: no topology graph")
)
