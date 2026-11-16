package hub

import "github.com/afonsodelgado/factory/internal/review"

func (m *Model) servePhoneReview() {
	if !m.Config.Phone.Enabled || m.Review == nil || m.Review.ServesMobile() {
		return
	}
	if err := m.Review.ServeMobile(review.MobileSocket(m.Config.StateDir)); err != nil {
		m.notice = problem(err.Error())
	}
}
