package conversion

import "danny.vn/banhmi/internal/corpus/conversion/sandbox"

func resourceFailure(dimension string) error {
	bound := sandbox.BoundOutput
	switch dimension {
	case "input":
		bound = sandbox.BoundInput
	case "expanded":
		bound = sandbox.BoundExpanded
	case "items":
		bound = sandbox.BoundMembers
	case "depth":
		bound = sandbox.BoundXML
	case "ratio":
		bound = sandbox.BoundRatio
	case "files":
		bound = sandbox.BoundFiles
	}
	return &sandbox.Failure{Cause: sandbox.ResourceLimit, Bound: bound}
}
