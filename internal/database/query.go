package database

import (
	"gorm.io/gen/field"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Project accepts only ORM expressions, including library aggregate functions.
func Project(db *gorm.DB, fields ...field.Expr) *gorm.DB {
	expressions := make([]clause.Expression, len(fields))
	for i, f := range fields {
		expressions[i] = f
	}
	return db.Clauses(clause.Select{Expression: clause.CommaExpression{Exprs: expressions}})
}

func Group(db *gorm.DB, names ...string) *gorm.DB {
	columns := make([]clause.Column, len(names))
	for i, name := range names {
		columns[i] = clause.Column{Name: name}
	}
	return db.Clauses(clause.GroupBy{Columns: columns})
}

func Asc(name string) clause.OrderByColumn {
	return clause.OrderByColumn{Column: clause.Column{Name: name}}
}
func Desc(name string) clause.OrderByColumn {
	return clause.OrderByColumn{Column: clause.Column{Name: name}, Desc: true}
}
