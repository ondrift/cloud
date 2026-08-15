// route.go — where a canvas site mounts, and the directory name derived from
// it. One implementation, read by the Driftfile reconcile, by the local `run`
// layout and by `drift canvas deploy`, so a site cannot land under one slug on
// a laptop and a different one on the slice.
package common

import (
	"errors"
	"strings"
)

// CanonicalRoute normalises a site's mount path: it begins with a slash, carries
// no trailing one, and "/" is the root.
//
// An empty value is refused rather than read as the root. A caller that means
// the root says so; a caller holding a value it could not read gets an error
// here, because the alternative is every site on the project agreeing on the
// slug `default` and overwriting each other one upload at a time.
func CanonicalRoute(route string) (string, error) {
	if strings.TrimSpace(route) == "" {
		return "", errors.New("mount path is empty — give a path like /admin, or leave the key out to mount at the root")
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	route = strings.TrimRight(route, "/")
	if route == "" {
		return "/", nil
	}
	return route, nil
}

// SlugifyRoute derives the per-site directory name from a mount path. The slug
// is what the slice lays sites out under, at /data/canvas/<slug>/.
//
//	"/"               -> "default"
//	"/reviewer"       -> "reviewer"
//	"/admin/portal"   -> "admin-portal"
func SlugifyRoute(route string) (string, error) {
	r, err := CanonicalRoute(route)
	if err != nil {
		return "", err
	}
	if r == "/" {
		return "default", nil
	}
	return strings.ReplaceAll(strings.TrimPrefix(r, "/"), "/", "-"), nil
}
