package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdinit"
)

func TestBuildInitCmd_Exists(t *testing.T) {
	cmd := cmdinit.BuildInitCmd()
	require.NotNil(t, cmd)
	assert.Equal(t, "init", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestInitCmd_RegisteredInRoot(t *testing.T) {
	root := newRootCmd()
	var found bool
	for _, c := range root.Commands() {
		if c.Use == "init" {
			found = true
			assert.Equal(t, "utility", c.GroupID)
			break
		}
	}
	assert.True(t, found, "init command should be registered under root")
}
