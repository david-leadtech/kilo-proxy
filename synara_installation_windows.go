package main

import "os"

func synaraOpenCandidate(path string) (*os.File, error) { return os.Open(path) }
