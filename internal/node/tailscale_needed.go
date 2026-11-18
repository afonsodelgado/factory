package node

import "fmt"

const AddTailscale = "factory setup --tailscale"

type TailscaleFeature string

const (
	PhoneFeature TailscaleFeature = "the phone app needs Tailscale"
	NodesFeature TailscaleFeature = "other machines join over Tailscale"
)

func NeedsTailscale(feature TailscaleFeature, machine string) string {
	return fmt.Sprintf("%s, and %s has not added it", feature, machine)
}
