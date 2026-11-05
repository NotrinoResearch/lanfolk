//go:build !windows

package main

func endHungInstance(string, string) bool { return false }
