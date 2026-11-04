(() => {
  const script = document.currentScript;
  if (!script || window.__factoryReviewLoaded) return;
  window.__factoryReviewLoaded = true;

  const page = script.dataset.page;
  const scrollKey = "factory-review-scroll:" + page;

  try {
    const saved = sessionStorage.getItem(scrollKey);
    if (saved !== null) {
      sessionStorage.removeItem(scrollKey);
      const y = Number(saved);
      addEventListener("load", () => scrollTo(0, y), { once: true });
    }
  } catch {}

  let reloadTimer;
  const events = new EventSource(script.dataset.events);
  events.addEventListener("change", (e) => {
    if (e.data !== page) return;
    clearTimeout(reloadTimer);
    reloadTimer = setTimeout(() => {
      try {
        sessionStorage.setItem(scrollKey, String(scrollY));
      } catch {}
      location.reload();
    }, 150);
  });
})();
