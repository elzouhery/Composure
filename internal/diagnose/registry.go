package diagnose

// The rule registry.
//
// One rule per file, listed here once. Adding a rule is adding a file and one
// line in this slice — there is no switch to extend, no type to widen and no
// existing rule to touch, which is what story 3.1's last acceptance criterion
// asks for. The order is the order rules run in; findings are sorted afterwards
// by severity and position, so it affects nothing but readability of this list.
var rules = []rule{
	credentialsRule{},       // 3.1
	portCollisionRule{},     // 3.2
	healthcheckRule{},       // 3.3
	cycleRule{},             // 3.4
	unusedResourceRule{},    // 3.5
	undefinedVarRule{},      // 3.6
	unreachableRule{},       // 3.7
	versionFieldRule{},      // 5.2 — the obsolete top-level `version:` field
	missingDockerfileRule{}, // 6.3 — a build naming a Dockerfile that is not there
	formMismatchRule{},      // configuration the merge dropped across a list/mapping mismatch
}
