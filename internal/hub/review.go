package hub

import (
	"github.com/afonsodelgado/factory/internal/review"
	"github.com/afonsodelgado/factory/internal/state"
)

func (m *Model) syncReview() {
	if m.Review == nil {
		return
	}
	factories := make([]review.Factory, 0, len(m.snap.Items))
	for _, it := range m.snap.Items {
		factories = append(factories, review.Factory{Key: state.FileStem(it.Factory.Repo, it.Factory.Slug), Label: it.Factory.Label(), Dir: it.Factory.Dir})
	}
	m.Review.SetFactories(factories)
}

func (m *Model) reviewURL(url string) string {
	if m.Review == nil {
		return url
	}
	if page, ok := m.Review.PageURL(url); ok {
		return page
	}
	return url
}

func (m *Model) threadCounter() threadCounter {
	if m.Review == nil {
		return nil
	}
	return m.Review.OpenCount
}
