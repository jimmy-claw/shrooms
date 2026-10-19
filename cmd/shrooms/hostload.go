package main

import "github.com/vpavlin/shrooms/internal/hostload"

// expo writes hostload's samples as the rest of the daemon's metrics.
func (e *expo) Family(name, typ, help string)                   { e.family(name, typ, help) }
func (e *expo) Sample(name string, v float64, labels ...string) { e.sample(name, v, labels...) }

func hostLoad(e *expo, p hostload.Paths) { hostload.Write(e, p) }
