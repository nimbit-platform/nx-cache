package web

import "embed"

//go:embed static/*
var staticRoot embed.FS
