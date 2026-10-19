package agent

// What a session is for (ADR-063): its skills, what it is good at, as A2A's
// own AgentSkill on its card; and its responsibilities, what it owns and may
// do without asking the owner, as this project's card extension. Set by the
// owner as the model is; a caged session's socket does not reach its
// settings, so it cannot give itself either.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Skill is one thing a session is good at, as A2A's AgentSkill names it.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

const (
	maxSkills       = 20
	maxDuties       = 20
	maxSkillName    = 60
	maxSkillText    = 300
	maxSkillTags    = 8
	maxSkillTagSize = 30
	maxDutyText     = 200
)

// responsibilitiesExtension is the card extension that carries them.
const responsibilitiesExtension = "https://github.com/vpavlin/shrooms/blob/master/docs/adr/063-what-each-agent-is-for.md"

func oneLine(s string, max int, what string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\n\r") {
		return "", fmt.Errorf("%s is one line", what)
	}
	if utf8.RuneCountInString(s) > max {
		return "", fmt.Errorf("%s is longer than %d characters", what, max)
	}
	return s, nil
}

// cleanRoles checks and tidies what the owner sets: names given, lines
// short, no duplicates.
func cleanRoles(skills []Skill, duties []string) ([]Skill, []string, error) {
	if len(skills) > maxSkills || len(duties) > maxDuties {
		return nil, nil, fmt.Errorf("at most %d skills and %d responsibilities", maxSkills, maxDuties)
	}
	var out []Skill
	seen := map[string]bool{}
	for _, sk := range skills {
		name, err := oneLine(sk.Name, maxSkillName, "a skill's name")
		if err != nil {
			return nil, nil, err
		}
		if name == "" {
			return nil, nil, fmt.Errorf("a skill needs a name")
		}
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		desc, err := oneLine(sk.Description, maxSkillText, "a skill's description")
		if err != nil {
			return nil, nil, err
		}
		if len(sk.Tags) > maxSkillTags {
			return nil, nil, fmt.Errorf("at most %d tags on a skill", maxSkillTags)
		}
		var tags []string
		for _, t := range sk.Tags {
			t, err := oneLine(t, maxSkillTagSize, "a tag")
			if err != nil {
				return nil, nil, err
			}
			if t != "" && !strings.ContainsAny(t, " \t") {
				tags = append(tags, strings.ToLower(t))
			}
		}
		out = append(out, Skill{Name: name, Description: desc, Tags: tags})
	}
	var ds []string
	for _, d := range duties {
		d, err := oneLine(d, maxDutyText, "a responsibility")
		if err != nil {
			return nil, nil, err
		}
		if d != "" {
			ds = append(ds, d)
		}
	}
	return out, ds, nil
}

// SetRoles sets what the session is for; nil leaves that part as it is.
func (s *Session) SetRoles(skills *[]Skill, duties *[]string, by string) error {
	var sk []Skill
	var ds []string
	if skills != nil {
		sk = *skills
	}
	if duties != nil {
		ds = *duties
	}
	sk, ds, err := cleanRoles(sk, ds)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if skills != nil {
		s.skills = sk
	}
	if duties != nil {
		s.duties = ds
	}
	s.record("setting", by, map[string]any{"skills": s.skills, "responsibilities": s.duties})
	s.mu.Unlock()
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.m.save()
}
