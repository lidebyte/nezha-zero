(function () {
  const closeLabel = document.currentScript.dataset.closeLabel;
  const mobileMedia = window.matchMedia('(max-width: 767px)');

  function updateCloseButtons() {
    document.querySelectorAll('.ui.modal').forEach(function (modal) {
      if (!modal.querySelector('form, .ui.form')) return;
      const existing = modal.querySelector('.dashboard-mobile-close');
      if (!mobileMedia.matches) {
        if (existing) existing.remove();
        modal.classList.remove('dashboard-mobile-closable');
        return;
      }
      if (existing) return;

      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'dashboard-mobile-close';
      button.setAttribute('aria-label', closeLabel);
      button.title = closeLabel;
      const icon = document.createElement('i');
      icon.className = 'times icon';
      icon.setAttribute('aria-hidden', 'true');
      button.appendChild(icon);
      button.addEventListener('click', function () {
        const cancel = modal.querySelector('.actions .deny.button, .actions .negative.button, .actions .cancel.button');
        if (cancel) {
          cancel.click();
        } else {
          $(modal).modal('hide');
        }
      });
      modal.classList.add('dashboard-mobile-closable');
      modal.appendChild(button);
    });
  }

  document.addEventListener('DOMContentLoaded', updateCloseButtons);
  mobileMedia.addEventListener('change', updateCloseButtons);
})();
