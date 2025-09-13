package geminiwebapi

import (
    "html"
    "fmt"
)

type Candidate struct {
    RCID            string
    Text            string
    Thoughts        *string
    WebImages       []WebImage
    GeneratedImages []GeneratedImage
}

func (c Candidate) String() string {
    t := c.Text
    if len(t) > 20 { t = t[:20] + "..." }
    return fmt.Sprintf("Candidate(rcid='%s', text='%s', images=%d)", c.RCID, t, len(c.WebImages)+len(c.GeneratedImages))
}

func (c Candidate) Images() []Image {
    images := make([]Image, 0, len(c.WebImages)+len(c.GeneratedImages))
    for _, wi := range c.WebImages { images = append(images, wi.Image) }
    for _, gi := range c.GeneratedImages { images = append(images, gi.Image) }
    return images
}

func decodeHTML(s string) string { return html.UnescapeString(s) }

