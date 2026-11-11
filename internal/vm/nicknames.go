package vm

import (
	"crypto/rand"
	"math/big"
)

// SandboxNicknames contains 100 curated short, memorable nicknames for sandbox instances.
var SandboxNicknames = []string{
	"doom", "runner", "pusher", "trove", "neutron", "punisher", "vortex", "cipher",
	"phantom", "blaze", "titan", "bolt", "apex", "spark", "fury", "zenith",
	"nova", "echo", "shadow", "viper", "raptor", "forge", "haven", "pulse",
	"quarry", "strider", "tempest", "crusher", "specter", "warden", "atlas", "beacon",
	"cinder", "drift", "ember", "falcon", "glitch", "hazard", "ignite", "jaguar",
	"krypton", "lumen", "matrix", "nexus", "orbit", "prism", "quantum", "rebel",
	"siren", "tracker", "ultra", "vector", "wraith", "xenon", "yield", "zero",
	"arcade", "bullet", "comet", "dagger", "dynamo", "flint", "gargoyle", "hunter",
	"inferno", "jolt", "kraken", "laser", "magma", "neon", "onyx", "proton",
	"quasar", "rocket", "saber", "turbo", "valkyrie", "warp", "bastion", "clutch",
	"diesel", "frost", "havoc", "iron", "katana", "lynx", "mirage", "nitro",
	"omega", "pyro", "radar", "stealth", "trigger", "vanguard", "wildcat", "zephyr",
	"torque", "striker", "tracer", "scout",
}

// RandomNickname selects a random short nickname from SandboxNicknames.
func RandomNickname() string {
	if len(SandboxNicknames) == 0 {
		return "sandbox"
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(SandboxNicknames))))
	if err != nil {
		return SandboxNicknames[0]
	}
	return SandboxNicknames[n.Int64()]
}
