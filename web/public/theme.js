try {
  const theme = localStorage.getItem("pulsemedia.theme");
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    document.querySelector('meta[name="theme-color"]').setAttribute("content", theme === "light" ? "#f4f6f5" : "#171c1b");
  }
} catch { /* Use the default theme when browser storage is unavailable. */ }
