package hub

import "github.com/afonsodelgado/factory/internal/host"

type GitHub = host.GitHub

const ghEvery = host.GHEvery

func RunGH(dir string, args ...string) ([]byte, error) { return host.RunGH(dir, args...) }
