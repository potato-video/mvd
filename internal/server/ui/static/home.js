// The home page's Up Next list: a click or focus on a row features it; a
// click on the row already featured plays it, as does Enter; ↑ and ↓ move
// along. The page re-renders after a clear or a removal (htmx swaps #home,
// running this again), so the choice is kept and put back.
(function () {
  const st = window.mvdHome || (window.mvdHome = { key: null });

  const rows = () => Array.from(document.querySelectorAll('#home .qrow'));

  function select(row, focus, reveal) {
    const home = document.getElementById('home');
    if (!home || !row) return;
    for (const r of rows()) {
      const on = r === row;
      r.setAttribute('aria-selected', on ? 'true' : 'false');
      r.tabIndex = on ? 0 : -1;
    }
    for (const f of home.querySelectorAll('.feature')) f.hidden = f.id !== 'f-' + row.dataset.key;
    st.key = row.dataset.key;
    // Stacked (a phone), the feature is above the list: bring it into view.
    const feat = home.querySelector('.features');
    if (reveal && feat && feat.getBoundingClientRect().bottom < 40 && matchMedia('(max-width: 899px)').matches) {
      feat.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    if (focus) {
      row.focus();
      row.scrollIntoView({ block: 'nearest' });
    }
  }

  const play = (row) => { location.href = row.dataset.play; };
  const inControl = (e) => e.target.closest('button, a');

  if (!st.bound) {
    st.bound = true;
    let wasOn = false; // the row was featured before this click's press focused it
    document.addEventListener('pointerdown', (e) => {
      const row = e.target.closest('#home .qrow');
      wasOn = !!row && row.getAttribute('aria-selected') === 'true';
    });
    document.addEventListener('click', (e) => {
      const row = e.target.closest('#home .qrow');
      if (!row || inControl(e)) return;
      if (wasOn) play(row); else select(row, false, true);
      wasOn = false;
    });
    document.addEventListener('focusin', (e) => {
      const row = e.target.closest && e.target.closest('#home .qrow');
      if (row && e.target === row) select(row);
    });
    document.addEventListener('keydown', (e) => {
      const row = e.target.closest && e.target.closest('#home .qrow');
      if (!row || e.target !== row) return;
      const all = rows(), i = all.indexOf(row);
      let to = null;
      switch (e.key) {
        case 'ArrowDown': to = all[i + 1]; break;
        case 'ArrowUp': to = all[i - 1]; break;
        case 'Home': to = all[0]; break;
        case 'End': to = all[all.length - 1]; break;
        case 'Enter': case ' ': e.preventDefault(); play(row); return;
        default: return;
      }
      e.preventDefault();
      if (to) select(to, true);
    });
  }

  // Back after a re-render: feature what was featured, if it's still here.
  const again = st.key && document.getElementById('c-' + st.key);
  if (again) select(again);
})();
