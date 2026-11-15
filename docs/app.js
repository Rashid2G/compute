(function () {
  const root = document.documentElement;
  const themeBtn = document.getElementById("theme-toggle");
  const themeLabel = document.getElementById("theme-label");
  const searchInput = document.getElementById("doc-search");
  const searchEmpty = document.getElementById("search-empty");
  const sidebar = document.getElementById("doc-sidebar");
  const menuBtn = document.getElementById("mobile-menu-btn");

  // 1. Theme toggle
  function applyTheme(theme) {
    if (theme === "dark") {
      root.classList.add("dark");
      if (themeLabel) themeLabel.textContent = "Light";
    } else {
      root.classList.remove("dark");
      if (themeLabel) themeLabel.textContent = "Dark";
    }
    try {
      localStorage.setItem("compute_docs_theme", theme);
    } catch (_) {}
  }

  const savedTheme = (() => {
    try {
      return localStorage.getItem("compute_docs_theme");
    } catch (_) {
      return null;
    }
  })();

  if (savedTheme === "dark" || savedTheme === "light") {
    applyTheme(savedTheme);
  } else if (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches) {
    applyTheme("dark");
  } else {
    applyTheme("dark");
  }

  if (themeBtn) {
    themeBtn.addEventListener("click", () => {
      applyTheme(root.classList.contains("dark") ? "light" : "dark");
    });
  }

  // 2. Mobile drawer
  if (menuBtn && sidebar) {
    menuBtn.addEventListener("click", () => {
      sidebar.classList.toggle("open");
    });
    sidebar.addEventListener("click", (e) => {
      if (e.target && e.target.closest("a.nav-link")) {
        sidebar.classList.remove("open");
      }
    });
  }

  // 3. Copy-to-clipboard buttons on code blocks
  document.querySelectorAll(".code-block").forEach((block) => {
    const btn = block.querySelector(".copy-btn");
    const codeEl = block.querySelector("pre code");
    if (!btn || !codeEl) return;

    btn.addEventListener("click", async () => {
      const text = codeEl.innerText;
      try {
        await navigator.clipboard.writeText(text);
        btn.textContent = "Copied";
        btn.classList.add("copied");
        setTimeout(() => {
          btn.textContent = "Copy";
          btn.classList.remove("copied");
        }, 1600);
      } catch (_) {
        const ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        btn.textContent = "Copied";
        btn.classList.add("copied");
        setTimeout(() => {
          btn.textContent = "Copy";
          btn.classList.remove("copied");
        }, 1600);
      }
    });
  });

  // 4. Client-side search & filter
  const sections = Array.from(document.querySelectorAll(".doc-section"));
  const navGroups = Array.from(document.querySelectorAll(".nav-group"));
  const navLinks = Array.from(document.querySelectorAll(".nav-link"));

  function runFilter(query) {
    const q = query.trim().toLowerCase();
    const visibleIds = new Set();

    sections.forEach((sec) => {
      if (!q) {
        sec.classList.remove("search-dimmed");
        visibleIds.add(sec.id);
        return;
      }
      const haystack = (sec.getAttribute("data-keywords") || "") + " " + sec.textContent;
      const match = haystack.toLowerCase().includes(q);
      sec.classList.toggle("search-dimmed", !match);
      if (match) visibleIds.add(sec.id);
    });

    let totalNavMatches = 0;
    navGroups.forEach((group) => {
      let groupVisible = 0;
      group.querySelectorAll(".nav-link").forEach((link) => {
        const href = (link.getAttribute("href") || "").replace(/^#/, "");
        const show = !q || visibleIds.has(href);
        link.classList.toggle("is-hidden", !show);
        if (show) {
          groupVisible++;
          totalNavMatches++;
        }
      });
      group.classList.toggle("is-hidden", groupVisible === 0);
    });

    if (searchEmpty) {
      searchEmpty.classList.toggle("visible", Boolean(q && totalNavMatches === 0));
    }
  }

  if (searchInput) {
    searchInput.addEventListener("input", (e) => {
      runFilter(e.target.value);
    });

    document.addEventListener("keydown", (e) => {
      if (
        (e.key === "/" || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k")) &&
        document.activeElement !== searchInput &&
        !["INPUT", "TEXTAREA"].includes(document.activeElement?.tagName || "")
      ) {
        e.preventDefault();
        searchInput.focus();
        searchInput.select();
      } else if (e.key === "Escape" && document.activeElement === searchInput) {
        searchInput.value = "";
        runFilter("");
        searchInput.blur();
      }
    });
  }

  // 5. Scroll-spy active nav link
  if ("IntersectionObserver" in window && sections.length > 0) {
    const linkById = new Map();
    navLinks.forEach((link) => {
      const id = (link.getAttribute("href") || "").replace(/^#/, "");
      if (id) linkById.set(id, link);
    });

    const observer = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            navLinks.forEach((l) => l.classList.remove("active"));
            const active = linkById.get(entry.target.id);
            if (active) active.classList.add("active");
          }
        });
      },
      { rootMargin: "-20% 0px -70% 0px", threshold: 0 }
    );

    sections.forEach((sec) => observer.observe(sec));
  }
})();
