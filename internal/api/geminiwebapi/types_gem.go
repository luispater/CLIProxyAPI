package geminiwebapi

import "fmt"

type Gem struct {
    ID          string
    Name        string
    Description *string
    Prompt      *string
    Predefined  bool
}

func (g Gem) String() string {
    return fmt.Sprintf("Gem(id='%s', name='%s', description='%v', prompt='%v', predefined=%v)", g.ID, g.Name, g.Description, g.Prompt, g.Predefined)
}

// GemJar is a simple map with helpers
type GemJar map[string]Gem

func (gj GemJar) Iter() []Gem {
    out := make([]Gem, 0, len(gj))
    for _, v := range gj { out = append(out, v) }
    return out
}

func (gj GemJar) Get(id *string, name *string, def *Gem) *Gem {
    if id == nil && name == nil { return def }
    if id != nil {
        if g, ok := gj[*id]; ok {
            if name != nil {
                if g.Name == *name { return &g } else { return def }
            }
            return &g
        }
        return def
    } else if name != nil {
        for _, g := range gj { if g.Name == *name { gg := g; return &gg } }
        return def
    }
    return def
}

func (gj GemJar) Filter(predefined *bool, name *string) GemJar {
    out := GemJar{}
    for k, g := range gj {
        if predefined != nil && g.Predefined != *predefined { continue }
        if name != nil && g.Name != *name { continue }
        out[k] = g
    }
    return out
}

