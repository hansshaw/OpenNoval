// Package mobilebridge is the public gomobile binding surface. Keep the
// implementation internal while allowing gobind's generated module to import
// only this package.
package mobilebridge

import core "github.com/hansshaw/AInoval-web-ui/internal/mobilebridge"

type WorkObserver interface {
	OnWorkStarted()
}

func Start(dataDirectory, cacheDirectory string) (string, error) {
	return core.Start(dataDirectory, cacheDirectory)
}

func StartWithObserver(dataDirectory, cacheDirectory string, observer WorkObserver) (string, error) {
	return core.StartWithObserver(dataDirectory, cacheDirectory, observer)
}

func HasActiveWork() bool { return core.HasActiveWork() }

func Stop() { core.Stop() }

func URL() string { return core.URL() }
