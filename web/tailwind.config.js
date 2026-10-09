/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ['class'],
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    container: {
      center: true,
      padding: '1rem',
      screens: { '2xl': '1400px' },
    },
    extend: {
      colors: {
        border: 'rgb(var(--border) / <alpha-value>)',
        input: 'rgb(var(--input) / <alpha-value>)',
        ring: 'rgb(var(--ring) / <alpha-value>)',
        background: 'rgb(var(--background) / <alpha-value>)',
        foreground: 'rgb(var(--foreground) / <alpha-value>)',
        primary: {
          DEFAULT: 'rgb(var(--primary) / <alpha-value>)',
          foreground: 'rgb(var(--primary-foreground) / <alpha-value>)',
        },
        secondary: {
          DEFAULT: 'rgb(var(--secondary) / <alpha-value>)',
          foreground: 'rgb(var(--secondary-foreground) / <alpha-value>)',
        },
        muted: {
          DEFAULT: 'rgb(var(--muted) / <alpha-value>)',
          foreground: 'rgb(var(--muted-foreground) / <alpha-value>)',
        },
        accent: {
          DEFAULT: 'rgb(var(--accent) / <alpha-value>)',
          foreground: 'rgb(var(--accent-foreground) / <alpha-value>)',
        },
        card: {
          DEFAULT: 'rgb(var(--card) / <alpha-value>)',
          foreground: 'rgb(var(--card-foreground) / <alpha-value>)',
        },
        popover: {
          DEFAULT: 'rgb(var(--popover) / <alpha-value>)',
          foreground: 'rgb(var(--popover-foreground) / <alpha-value>)',
        },
        success: {
          DEFAULT: 'rgb(var(--success) / <alpha-value>)',
          foreground: 'rgb(var(--success-foreground) / <alpha-value>)',
        },
        warning: {
          DEFAULT: 'rgb(var(--warning) / <alpha-value>)',
          foreground: 'rgb(var(--warning-foreground) / <alpha-value>)',
        },
        danger: {
          DEFAULT: 'rgb(var(--danger) / <alpha-value>)',
          foreground: 'rgb(var(--danger-foreground) / <alpha-value>)',
        },
        destructive: {
          DEFAULT: 'rgb(var(--danger) / <alpha-value>)',
          foreground: 'rgb(var(--danger-foreground) / <alpha-value>)',
        },
      },
      borderRadius: {
        none: '0px',
        sm: '8px',
        DEFAULT: '12px',
        md: '12px',
        lg: '16px',
        xl: '24px',
        '2xl': '24px',
        '3xl': '32px',
        full: '9999px',
      },
      fontFamily: {
        sans: [
          'Inter',
          'PingFang SC',
          'Microsoft YaHei',
          'system-ui',
          '-apple-system',
          'Segoe UI',
          'sans-serif',
        ],
        mono: [
          'JetBrains Mono',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'Consolas',
          'monospace',
        ],
      },
      fontSize: {
        '2xs': ['11px', { lineHeight: '1.4' }],
        xs: ['12px', { lineHeight: '1.5' }],
        sm: ['13px', { lineHeight: '1.5' }],
        base: ['14px', { lineHeight: '1.6' }],
        md: ['14px', { lineHeight: '1.6' }],
        lg: ['16px', { lineHeight: '1.4' }],
        xl: ['20px', { lineHeight: '1.3' }],
        '2xl': ['24px', { lineHeight: '1.25' }],
        '3xl': ['30px', { lineHeight: '1.2' }],
        '4xl': ['32px', { lineHeight: '1.1' }],
      },
      boxShadow: {
        sm: '0 1px 2px rgba(0,0,0,.05)',
        md: '0 4px 12px rgba(0,0,0,.08)',
        lg: '0 12px 32px rgba(0,0,0,.12)',
      },
      spacing: {
        18: '4.5rem',
        22: '5.5rem',
      },
      keyframes: {
        'fade-in': {
          from: { opacity: '0' },
          to: { opacity: '1' },
        },
        'slide-up': {
          from: { opacity: '0', transform: 'translateY(8px)' },
          to: { opacity: '1', transform: 'translateY(0)' },
        },
      },
      animation: {
        'fade-in': 'fade-in 150ms ease-out',
        'slide-up': 'slide-up 150ms ease-out',
      },
    },
  },
  plugins: [require('tailwindcss-animate')],
}
