package geminiwebapi

type ModelOutput struct {
    Metadata  []string
    Candidates []Candidate
    Chosen    int
}

func (m ModelOutput) String() string { return m.Text() }

func (m ModelOutput) Text() string {
    if len(m.Candidates) == 0 { return "" }
    return m.Candidates[m.Chosen].Text
}

func (m ModelOutput) Thoughts() *string {
    if len(m.Candidates) == 0 { return nil }
    return m.Candidates[m.Chosen].Thoughts
}

func (m ModelOutput) Images() []Image {
    if len(m.Candidates) == 0 { return nil }
    return m.Candidates[m.Chosen].Images()
}

func (m ModelOutput) RCID() string {
    if len(m.Candidates) == 0 { return "" }
    return m.Candidates[m.Chosen].RCID
}

