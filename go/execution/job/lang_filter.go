package job

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"

// LangTag identifies which language stack a job belongs to.
type LangTag string

const (
	// LangGo tags jobs that belong to the Go stack.
	LangGo LangTag = "go"

	// LangPython tags jobs that belong to the Python stack.
	LangPython LangTag = "python"

	// LangTS tags jobs that belong to the TypeScript stack.
	LangTS LangTag = "ts"

	// LangInfra covers Docker, k8s, proto, and shellcheck jobs that must run
	// regardless of which language-specific flags are set.
	LangInfra LangTag = "infra"
)

// TaggedJob pairs an AnyJob with its language tag.
type TaggedJob struct {
	// AnyJob is the embedded job being tagged; its methods are promoted.
	interfaces.AnyJob

	// Lang is the language stack this job belongs to, used by FilterByLang.
	Lang LangTag
}

// Tag wraps j with a language tag.
func Tag(lang LangTag, j interfaces.AnyJob) TaggedJob {
	return TaggedJob{AnyJob: j, Lang: lang}
}

// FilterByLang returns the jobs whose LangTag matches the enabled flags.
// LangInfra jobs are always included - they validate shared infrastructure
// (Docker, k8s, proto) that must pass regardless of which language changed.
func FilterByLang(jobs []TaggedJob, goFlag, pyFlag, tsFlag bool) []interfaces.AnyJob {
	out := make([]interfaces.AnyJob, 0, len(jobs))
	for _, tj := range jobs {
		switch tj.Lang {
		case LangGo:
			if goFlag {
				out = append(out, tj.AnyJob)
			}
		case LangPython:
			if pyFlag {
				out = append(out, tj.AnyJob)
			}
		case LangTS:
			if tsFlag {
				out = append(out, tj.AnyJob)
			}
		case LangInfra:
			out = append(out, tj.AnyJob)
		}
	}
	return out
}
